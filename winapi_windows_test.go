//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// Cada goroutine presa a uma thread faz o papel de uma copia do programa: o dono de um
// mutex no Windows e a thread, entao isso reproduz duas copias de verdade.
func TestSecondCopyWaitsForTurnInsteadOfQuitting(t *testing.T) {
	name := fmt.Sprintf(`Local\DiscordProtonTest-%d`, os.Getpid())

	holding := make(chan struct{})
	letGo := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		first, err := openMutex(name)
		if err != nil {
			t.Error(err)
			close(holding)
			return
		}
		if ok, err := first.acquire(0); !ok || err != nil {
			t.Errorf("a primeira copia nao pegou a vez: %v %v", ok, err)
		}
		close(holding)
		<-letGo
		first.release()
	}()
	<-holding

	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		second, err := openMutex(name)
		if err != nil {
			done <- err
			return
		}
		if ok, _ := second.acquire(0); ok {
			done <- fmt.Errorf("pegou a vez com a primeira copia ainda segurando")
			return
		}
		ok, err := second.acquire(5 * time.Second)
		if err == nil && !ok {
			err = fmt.Errorf("nao pegou a vez depois que a primeira soltou")
		}
		if ok {
			second.release()
		}
		done <- err
	}()

	time.Sleep(300 * time.Millisecond)
	close(letGo)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPowerShellQuote(t *testing.T) {
	if got := psQuote(`C:\Program Files\it's`); got != `'C:\Program Files\it''s'` {
		t.Fatalf("aspas mal escapadas: %s", got)
	}
}
