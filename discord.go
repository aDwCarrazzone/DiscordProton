package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type Channel struct {
	Key      string
	Folder   string
	Binary   string
	Friendly string
}

var channels = []Channel{
	{"stable", "Discord", "Discord.exe", "Discord"},
	{"ptb", "DiscordPTB", "DiscordPTB.exe", "Discord PTB"},
	{"canary", "DiscordCanary", "DiscordCanary.exe", "Discord Canary"},
}

func lookupChannel(key string) (Channel, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, channel := range channels {
		if channel.Key == key {
			return channel, nil
		}
	}
	return Channel{}, fmt.Errorf("canal desconhecido %q, use stable, ptb ou canary", key)
}

// Install e um canal instalado e o executavel da versao que esta valendo agora.
type Install struct {
	Channel Channel
	Root    string
	Exe     string
}

func (i Install) updater() string {
	return filepath.Join(i.Root, "Update.exe")
}

// Todo canal com uma versao completa. Canal que nao abre, por update interrompido ou
// pasta vazia de desinstalacao, fica de fora: nao ha caminho certo para pedir a Proton.
func detectInstalls(local string) []Install {
	var installs []Install
	for _, channel := range channels {
		root := filepath.Join(local, channel.Folder)
		exe, err := currentExe(root, channel)
		if err != nil {
			continue
		}
		installs = append(installs, Install{channel, root, exe})
	}
	return installs
}

// O Discord instala cada versao numa pasta app-<versao> nova e roda a partir dela. E
// esse caminho, nao o Discord.exe da raiz, que precisa estar na lista da Proton: o da
// raiz so dispara o Update.exe e sai.
func currentExe(root string, channel Channel) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("nao achei o %s em %s", channel.Friendly, root)
	}

	best, bestVersion := "", []int(nil)
	for _, entry := range entries {
		version, ok := parseVersion(entry.Name(), "app-")
		if !entry.IsDir() || !ok {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		exe := filepath.Join(dir, channel.Binary)
		if !fileExists(exe) || !launchable(dir) {
			continue
		}
		if best == "" || slices.Compare(version, bestVersion) > 0 {
			best, bestVersion = exe, version
		}
	}
	if best == "" {
		return "", fmt.Errorf("nenhuma versao completa do %s em %s", channel.Friendly, root)
	}
	return best, nil
}

// "app-1.0.9260" com prefixo "app-" vira [1 0 9260]. Comparado como numero, e nao como
// texto, 1.0.9260 fica a frente de 1.0.998.
func parseVersion(name, prefix string) ([]int, bool) {
	rest, found := strings.CutPrefix(name, prefix)
	if !found || rest == "" {
		return nil, false
	}
	fields := strings.Split(rest, ".")
	version := make([]int, len(fields))
	for i, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil {
			return nil, false
		}
		version[i] = n
	}
	return version, true
}

// Atualizacao que parou no meio deixa a pasta com o executavel mas sem os arquivos do
// Chromium; o Discord aberto dali fecha sozinho. Tem que ter os tres.
func launchable(dir string) bool {
	for _, need := range [...]string{"resources.pak", "icudtl.dat", "locales"} {
		if _, err := os.Stat(filepath.Join(dir, need)); err != nil {
			return false
		}
	}
	return true
}

// Abre pelo Update.exe, como o atalho original do Discord. Se ja estiver aberto, ele
// so traz a janela para frente.
func launchDiscord(install Install) error {
	cmd := exec.Command(install.updater(), "--processStart", install.Channel.Binary)
	cmd.Dir = install.Root
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func killDiscord(channel Channel) error {
	cmd := exec.Command("taskkill", "/F", "/T", "/IM", channel.Binary)
	cmd.SysProcAttr = hiddenProcess()
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	// O taskkill responde no idioma do Windows, entao "nao encontrado" se confere
	// olhando os processos, nao o texto.
	if running, _ := processPaths(channel.Binary); len(running) == 0 {
		return nil
	}
	return fmt.Errorf("taskkill %s: %v: %s", channel.Binary, err, strings.TrimSpace(string(out)))
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
