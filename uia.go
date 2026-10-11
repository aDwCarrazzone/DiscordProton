package main

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf16"
)

//go:embed uia.ps1
var uiaScript string

type uiaResult struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Connected bool   `json:"connected"`
}

func (r uiaResult) where() string {
	if r.Detail == "" {
		return r.Title
	}
	return r.Title + ", " + r.Detail
}

// O script vai por -EncodedCommand e nao por arquivo: nada em disco que outro processo
// possa trocar entre a escrita e a execucao.
func protonUI(action string, timeout time.Duration) (uiaResult, error) {
	launcher, _ := protonLauncher()
	script := fmt.Sprintf("$Action = %s\n$TimeoutSec = %d\n$Launcher = %s\n", psQuote(action), int(timeout.Seconds()), psQuote(launcher)) + uiaScript
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encodePowerShell(script))
	cmd.SysProcAttr = hiddenProcess()
	out, err := cmd.Output()

	line := lastJSONLine(string(out))
	if line == "" {
		if err != nil {
			return uiaResult{}, fmt.Errorf("automacao da Proton falhou: %w", err)
		}
		return uiaResult{}, errors.New("automacao da Proton nao respondeu")
	}
	var result uiaResult
	if err := json.Unmarshal([]byte(line), &result); err != nil {
		return uiaResult{}, fmt.Errorf("resposta da automacao ilegivel: %q", line)
	}
	if !result.OK {
		return result, errors.New(result.Error)
	}
	return result, nil
}

// Texto entre aspas simples no PowerShell nao expande nada; so a propria aspa simples
// precisa ser dobrada.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, len(units)*2)
	for i, u := range units {
		raw[2*i] = byte(u)
		raw[2*i+1] = byte(u >> 8)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func lastJSONLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "{") {
			return line
		}
	}
	return ""
}
