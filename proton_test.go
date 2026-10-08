package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Mesmo desenho do UserSettings real: CRLF, dois espacos, valores sempre texto,
// alguns cifrados com DPAPI, e a lista de apps como JSON dentro de texto.
const sampleSettings = "{\r\n" +
	`  "MaxDevicesAllowed": "11",` + "\r\n" +
	`  "VpnPlan": "AQAAAvalor+inventado/so+para/teste==",` + "\r\n" +
	`  "SplitTunnelingInverseAppsList": "[{\"AppFilePath\":\"C:\\\\Program Files\\\\Mozilla Firefox\\\\firefox.exe\",\"AlternateAppFilePaths\":[],\"IsActive\":false},{\"AppFilePath\":\"C:\\\\Users\\\\x\\\\AppData\\\\Local\\\\Discord\\\\Discord.exe\",\"AlternateAppFilePaths\":[],\"IsActive\":true},{\"AppFilePath\":\"C:\\\\Users\\\\x\\\\AppData\\\\Local\\\\Discord\\\\app-1.0.9259\\\\Discord.exe\",\"AlternateAppFilePaths\":[],\"IsActive\":true}]",` + "\r\n" +
	`  "SplitTunnelingMode": "Inverse",` + "\r\n" +
	`  "IsSplitTunnelingEnabled": "true",` + "\r\n" +
	`  "LastSeenWhatsNewOverlayVersion": "2"` + "\r\n" +
	"}"

func TestSettingsRoundTripIsByteIdentical(t *testing.T) {
	for _, data := range []string{sampleSettings, string(utf8BOM) + sampleSettings} {
		settings, err := parseProtonSettings([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if got := settings.encode(); !bytes.Equal(got, []byte(data)) {
			t.Fatalf("o arquivo mudou sem nenhuma edicao:\n%q\n%q", got, data)
		}
	}
}

// Confere contra o arquivo de verdade da maquina, sem gravar nada:
// DISCORDPROTON_REAL_SETTINGS=1 go test -run Real
func TestRealSettingsRoundTrip(t *testing.T) {
	if os.Getenv("DISCORDPROTON_REAL_SETTINGS") == "" {
		t.Skip("so roda com DISCORDPROTON_REAL_SETTINGS=1")
	}
	path, err := findProtonSettings(os.Getenv("LOCALAPPDATA"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := parseProtonSettings(data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(settings.encode(), data) {
		t.Fatal("o arquivo real nao volta identico")
	}
	list, err := settings.appList()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d chaves, %d apps na lista, modo incluir: %v", len(settings.keys), len(list.apps), settings.includeModeOn())
}

func TestIncludeModeRequired(t *testing.T) {
	settings, _ := parseProtonSettings([]byte(sampleSettings))
	if !settings.includeModeOn() {
		t.Fatal("modo Incluir ligado nao foi reconhecido")
	}
	settings.setStr(keyMode, "Standard")
	if settings.includeModeOn() {
		t.Fatal("modo Excluir passou como Incluir")
	}
}

func TestSyncAddsNewVersionAndDropsGoneOne(t *testing.T) {
	settings, _ := parseProtonSettings([]byte(sampleSettings))
	list, err := settings.appList()
	if err != nil {
		t.Fatal(err)
	}

	root := `C:\Users\x\AppData\Local\Discord`
	current := root + `\app-1.0.9260\Discord.exe`
	exists := func(path string) bool { return !strings.Contains(path, "9259") }

	plan := planSync(list.apps, []string{current}, []string{root}, exists)
	if len(plan.Add) != 1 || plan.Add[0] != current {
		t.Fatalf("versao nova nao entrou: %+v", plan)
	}
	if len(plan.Drop) != 1 || !strings.Contains(list.apps[plan.Drop[0]].AppFilePath, "9259") {
		t.Fatalf("versao que sumiu nao saiu: %+v", plan)
	}

	text, err := list.apply(plan)
	if err != nil {
		t.Fatal(err)
	}
	var apps []splitApp
	if err := json.Unmarshal([]byte(text), &apps); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, app := range apps {
		paths = append(paths, app.AppFilePath)
	}
	joined := strings.Join(paths, "|")
	if !strings.Contains(joined, "firefox.exe") || !strings.Contains(joined, `Discord\Discord.exe`) {
		t.Fatalf("itens que nao eram do Discord versionado foram mexidos: %v", paths)
	}
	if strings.Contains(joined, "9259") || !strings.Contains(joined, "9260") {
		t.Fatalf("lista final errada: %v", paths)
	}
	if apps[len(apps)-1].AlternateAppFilePaths == nil || !apps[len(apps)-1].IsActive {
		t.Fatalf("item novo fora do formato da Proton: %+v", apps[len(apps)-1])
	}

	// O texto precisa voltar para o arquivo como string JSON, e o arquivo continuar legivel.
	if err := settings.setStr(keyAppsList, text); err != nil {
		t.Fatal(err)
	}
	again, err := parseProtonSettings(settings.encode())
	if err != nil {
		t.Fatal(err)
	}
	reread, _ := again.appList()
	if !reread.active(current) {
		t.Fatal("caminho novo nao ficou ativo depois de gravar e reler")
	}
	if vpn, _ := again.str("VpnPlan"); vpn != "AQAAAvalor+inventado/so+para/teste==" {
		t.Fatalf("valor cifrado foi alterado: %q", vpn)
	}
}

func TestSyncReactivatesInsteadOfDuplicating(t *testing.T) {
	apps := []splitApp{{AppFilePath: `C:\D\app-1.0.1\Discord.exe`, IsActive: false}}
	plan := planSync(apps, []string{`c:\d\APP-1.0.1\discord.exe`}, []string{`C:\D`}, func(string) bool { return true })
	if len(plan.Add) != 0 || len(plan.Activate) != 1 {
		t.Fatalf("esperava reativar o item existente: %+v", plan)
	}
}

func TestSyncLeavesUnrelatedEntriesAlone(t *testing.T) {
	apps := []splitApp{
		{AppFilePath: `C:\Outro\app-1.0.0\Outro.exe`, IsActive: true},
		{AppFilePath: `C:\D\Update.exe`, IsActive: true},
	}
	plan := planSync(apps, nil, []string{`C:\D`}, func(string) bool { return false })
	if !plan.empty() {
		t.Fatalf("mexeu em item fora das pastas app-* do Discord: %+v", plan)
	}
}

func TestConnectedLine(t *testing.T) {
	line := `2026-01-01T12:00:00.000Z | INFO  | CONN.CONNECT:TRIGGER | [CONNECTION_PROCESS] Status updated to Connected. Connected to server US-NY#12 | {"Caller":"ConnectionManager.SetConnectionStatus:406"}`
	m := connectedLine.FindStringSubmatch(line)
	if m == nil || m[2] != "US-NY#12" {
		t.Fatalf("linha de conexao nao reconhecida: %v", m)
	}
}

func TestServerCountry(t *testing.T) {
	cases := map[string]string{
		"US-NY#12": "US",
		"PY#17":    "PY",
		"BR#3":     "BR",
		"CH-BR#1":  "BR",
		"IS-US#2":  "US",
		"VE#11":    "VE",
	}
	for server, want := range cases {
		if got := serverCountry(server); got != want {
			t.Errorf("%s: esperava %s, veio %s", server, want, got)
		}
	}
}
