package main

import (
	"os"
	"testing"
	"time"
)

// Le o cartao de conexao da Proton de verdade, sem apertar nada. Se a janela estiver
// escondida na bandeja, ela aparece:
// DISCORDPROTON_REAL_UI=1 go test -run RealUI -v
func TestRealUIState(t *testing.T) {
	if os.Getenv("DISCORDPROTON_REAL_UI") == "" {
		t.Skip("so roda com DISCORDPROTON_REAL_UI=1")
	}
	result, err := protonUI("state", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("conectado: %v, %s", result.Connected, result.where())
}

func TestLastJSONLineSkipsNoise(t *testing.T) {
	out := "aviso qualquer\r\n{\"ok\":true,\"title\":\"Estados Unidos\"}\r\n"
	if got := lastJSONLine(out); got != `{"ok":true,"title":"Estados Unidos"}` {
		t.Fatalf("linha JSON errada: %q", got)
	}
}
