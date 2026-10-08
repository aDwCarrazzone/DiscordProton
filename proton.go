package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	keyAppsList = "SplitTunnelingInverseAppsList"
	keyMode     = "SplitTunnelingMode"
	keyEnabled  = "IsSplitTunnelingEnabled"
	modeInclude = "Inverse"

	protonClientExe = "ProtonVPN.Client.exe"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// protonSettings guarda o UserSettings da Proton na ordem e nos bytes em que veio. So
// a lista de apps e reescrita: o resto inclui valores cifrados com DPAPI, que precisam
// voltar identicos.
type protonSettings struct {
	path   string
	bom    bool
	keys   []string
	values map[string]json.RawMessage
}

// Cada conta logada tem o seu arquivo. O mais recente e o da conta em uso.
func findProtonSettings(local string) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(local, "Proton", "Proton VPN", "Storage", "UserSettings.*.json"))
	if len(matches) == 0 {
		return "", errors.New("nao achei a configuracao da Proton VPN. Ela esta instalada e com uma conta logada?")
	}
	sort.Slice(matches, func(i, j int) bool { return modTime(matches[i]).After(modTime(matches[j])) })
	return matches[0], nil
}

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func loadProtonSettings(path string) (*protonSettings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	settings, err := parseProtonSettings(data)
	if err != nil {
		return nil, fmt.Errorf("configuracao da Proton em formato inesperado (%s): %w", path, err)
	}
	settings.path = path
	return settings, nil
}

func parseProtonSettings(data []byte) (*protonSettings, error) {
	s := &protonSettings{values: map[string]json.RawMessage{}}
	if bytes.HasPrefix(data, utf8BOM) {
		s.bom = true
		data = data[len(utf8BOM):]
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("nao comeca com um objeto")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("chave inesperada %v", tok)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if _, dup := s.values[key]; !dup {
			s.keys = append(s.keys, key)
		}
		s.values[key] = raw
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errors.New("objeto nao fecha")
	}
	return s, nil
}

// Mesmo desenho que a Proton grava: dois espacos, CRLF e nada depois da chave final.
func (s *protonSettings) encode() []byte {
	var b bytes.Buffer
	if s.bom {
		b.Write(utf8BOM)
	}
	b.WriteString("{\r\n")
	for i, key := range s.keys {
		name, _ := marshalPlain(key)
		b.WriteString("  ")
		b.Write(name)
		b.WriteString(": ")
		b.Write(s.values[key])
		if i < len(s.keys)-1 {
			b.WriteByte(',')
		}
		b.WriteString("\r\n")
	}
	b.WriteByte('}')
	return b.Bytes()
}

func (s *protonSettings) str(key string) (string, bool) {
	raw, ok := s.values[key]
	if !ok {
		return "", false
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return "", false
	}
	return *value, true
}

func (s *protonSettings) setStr(key, value string) error {
	raw, err := marshalPlain(value)
	if err != nil {
		return err
	}
	if _, ok := s.values[key]; !ok {
		s.keys = append(s.keys, key)
	}
	s.values[key] = raw
	return nil
}

// O incluir da Proton se chama Inverse por dentro. Em qualquer outro modo, por o
// Discord na lista faria o contrario: tiraria ele da VPN.
func (s *protonSettings) includeModeOn() bool {
	enabled, _ := s.str(keyEnabled)
	mode, _ := s.str(keyMode)
	return strings.EqualFold(enabled, "true") && strings.EqualFold(mode, modeInclude)
}

// marshalPlain nao escapa < > &: a Proton nao escapa, e caminho de Windows pode ter &.
func marshalPlain(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

type splitApp struct {
	AppFilePath           string   `json:"AppFilePath"`
	AlternateAppFilePaths []string `json:"AlternateAppFilePaths"`
	IsActive              bool     `json:"IsActive"`
}

// A lista vem como texto JSON dentro de um valor JSON. Os itens que nao mudam voltam
// com os bytes originais, para nao perder campo que esta versao nao conhece.
type appList struct {
	raw  []json.RawMessage
	apps []splitApp
}

func (s *protonSettings) appList() (appList, error) {
	text, ok := s.str(keyAppsList)
	if !ok || strings.TrimSpace(text) == "" {
		return appList{}, nil
	}
	var list appList
	if err := json.Unmarshal([]byte(text), &list.raw); err != nil {
		return appList{}, fmt.Errorf("lista de apps da Proton ilegivel: %w", err)
	}
	list.apps = make([]splitApp, len(list.raw))
	for i, raw := range list.raw {
		if err := json.Unmarshal(raw, &list.apps[i]); err != nil {
			return appList{}, fmt.Errorf("item %d da lista de apps ilegivel: %w", i, err)
		}
	}
	return list, nil
}

func (l appList) active(path string) bool {
	for _, app := range l.apps {
		if app.IsActive && app.covers(path) {
			return true
		}
	}
	return false
}

func (a splitApp) covers(path string) bool {
	if samePath(a.AppFilePath, path) {
		return true
	}
	for _, alt := range a.AlternateAppFilePaths {
		if samePath(alt, path) {
			return true
		}
	}
	return false
}

type syncPlan struct {
	Add      []string
	Activate []int
	Drop     []int
}

func (p syncPlan) empty() bool {
	return len(p.Add) == 0 && len(p.Activate) == 0 && len(p.Drop) == 0
}

// Garante cada caminho de want ativo e tira as versoes antigas do Discord que ja nao
// existem no disco. Nada fora das pastas app-* dos canais e removido: o resto da lista
// e escolha de quem usa a Proton.
func planSync(apps []splitApp, want []string, roots []string, exists func(string) bool) syncPlan {
	var plan syncPlan
	for _, path := range want {
		index := -1
		for i, app := range apps {
			if app.covers(path) {
				index = i
				break
			}
		}
		switch {
		case index < 0:
			plan.Add = append(plan.Add, path)
		case !apps[index].IsActive:
			plan.Activate = append(plan.Activate, index)
		}
	}

	for i, app := range apps {
		if versionedDiscordPath(app.AppFilePath, roots) && !exists(app.AppFilePath) {
			plan.Drop = append(plan.Drop, i)
		}
	}
	return plan
}

func versionedDiscordPath(path string, roots []string) bool {
	versionDir := filepath.Dir(path)
	if !strings.HasPrefix(strings.ToLower(filepath.Base(versionDir)), "app-") {
		return false
	}
	for _, root := range roots {
		if samePath(filepath.Dir(versionDir), root) {
			return true
		}
	}
	return false
}

func (l appList) apply(plan syncPlan) (string, error) {
	drop := map[int]bool{}
	for _, i := range plan.Drop {
		drop[i] = true
	}
	activate := map[int]bool{}
	for _, i := range plan.Activate {
		activate[i] = true
	}

	out := make([]json.RawMessage, 0, len(l.raw)+len(plan.Add))
	for i, raw := range l.raw {
		if drop[i] {
			continue
		}
		if activate[i] {
			app := l.apps[i]
			app.IsActive = true
			encoded, err := marshalPlain(app)
			if err != nil {
				return "", err
			}
			raw = encoded
		}
		out = append(out, raw)
	}
	for _, path := range plan.Add {
		encoded, err := marshalPlain(splitApp{AppFilePath: path, AlternateAppFilePaths: []string{}, IsActive: true})
		if err != nil {
			return "", err
		}
		out = append(out, encoded)
	}

	text, err := marshalPlain(out)
	if err != nil {
		return "", err
	}
	return string(text), nil
}

// Grava por arquivo temporario e rename: uma queda no meio deixa a configuracao antiga
// inteira, nunca metade de cada.
func (s *protonSettings) save(backupDir string) (string, error) {
	backup, err := backupFile(s.path, backupDir)
	if err != nil {
		return "", fmt.Errorf("nao consegui fazer backup da configuracao da Proton: %w", err)
	}
	tmp := s.path + ".discordproton.tmp"
	if err := os.WriteFile(tmp, s.encode(), 0o600); err != nil {
		return backup, err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return backup, err
	}
	return backup, nil
}

const keptBackups = 10

func backupFile(path, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer src.Close()

	name := strings.TrimSuffix(filepath.Base(path), ".json") + "-" + time.Now().Format("20060102-150405") + ".json"
	target := filepath.Join(dir, name)
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return "", err
	}
	if err := dst.Close(); err != nil {
		return "", err
	}

	old, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(old)
	for len(old) > keptBackups {
		os.Remove(old[0])
		old = old[1:]
	}
	return target, nil
}

func protonClientRunning() bool {
	paths, _ := processPaths(protonClientExe)
	return len(paths) > 0
}

// A Proton so le a configuracao quando abre. Fechada a forca ela nao grava nada por
// cima, e o tunel continua de pe: quem segura a conexao e o servico, nao a janela.
func stopProtonClient() error {
	cmd := exec.Command("taskkill", "/F", "/IM", protonClientExe)
	cmd.SysProcAttr = hiddenProcess()
	_ = cmd.Run()
	return waitUntil(15*time.Second, func() bool { return !protonClientRunning() }, "o app da Proton nao fechou")
}

// O ProtonVPN.Launcher.exe fica numa pasta fixa; o cliente de verdade mora em v<versao>
// e muda quando a Proton se atualiza. Chamar com o app ja aberto so mostra a janela.
func showProtonClient() error {
	exe, err := protonLauncher()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	cmd.Dir = filepath.Dir(exe)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func protonLauncher() (string, error) {
	base := filepath.Join(os.Getenv("ProgramFiles"), "Proton", "VPN")
	launcher := filepath.Join(base, "ProtonVPN.Launcher.exe")
	if _, err := os.Stat(launcher); err == nil {
		return launcher, nil
	}
	if paths, _ := processPaths(protonClientExe); len(paths) > 0 {
		return paths[0], nil
	}
	clients, _ := filepath.Glob(filepath.Join(base, "v*", protonClientExe))
	best, bestVersion := "", []int(nil)
	for _, client := range clients {
		version, ok := parseVersion(filepath.Base(filepath.Dir(client)), "v")
		if ok && (best == "" || slices.Compare(version, bestVersion) > 0) {
			best, bestVersion = client, version
		}
	}
	if best == "" {
		return "", fmt.Errorf("nao achei a Proton VPN em %s", base)
	}
	return best, nil
}

// Com a Proton conectada aparece o adaptador ProtonVPN com um IPv4 do tunel.
func tunnelUp() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifc := range ifaces {
		if !strings.HasPrefix(ifc.Name, "ProtonVPN") || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				return true
			}
		}
	}
	return false
}

// O log da Proton e a unica fonte que diz quando ela conectou de novo e em que servidor:
// o adaptador continua de pe numa reconexao rapida e nao serve para isso.
var connectedLine = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T[0-9:.]+Z) \| INFO\s*\| [A-Z.:]+ \| \[CONNECTION_PROCESS\] Status updated to Connected\. Connected to server (\S+)`)

const logTail = 512 << 10

func lastConnection(local string) (server string, at time.Time, ok bool) {
	path := filepath.Join(local, "Proton", "Proton VPN", "Logs", "client-logs.txt")
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, false
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > logTail {
		f.Seek(-logTail, io.SeekEnd)
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		m := connectedLine.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		when, err := time.Parse(time.RFC3339Nano, m[1])
		if err != nil {
			continue
		}
		server, at, ok = m[2], when, true
	}
	return server, at, ok
}

// Servidor Secure Core se chama ENTRADA-SAIDA#n e so entra por CH, IS ou SE. No resto,
// o que vem depois do hifen e estado ou cidade: US-NY#12 sai nos Estados Unidos.
var secureCoreEntries = map[string]bool{"CH": true, "IS": true, "SE": true}

func serverCountry(server string) string {
	name := strings.ToUpper(strings.SplitN(server, "#", 2)[0])
	parts := strings.Split(name, "-")
	if len(parts) >= 2 && secureCoreEntries[parts[0]] && len(parts[1]) == 2 {
		return parts[1]
	}
	return parts[0]
}

func waitUntil(timeout time.Duration, done func() bool, failure string) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if done() {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	if done() {
		return nil
	}
	return errors.New(failure)
}
