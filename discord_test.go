package main

import (
	"os"
	"path/filepath"
	"testing"
)

func makeVersion(t *testing.T, root, version, binary string, full bool) {
	t.Helper()
	dir := filepath.Join(root, "app-"+version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, binary), []byte("x"), 0o644)
	if !full {
		return
	}
	os.WriteFile(filepath.Join(dir, "icudtl.dat"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "resources.pak"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(dir, "locales"), 0o755)
}

func TestCurrentExePicksNewestCompleteVersion(t *testing.T) {
	root := t.TempDir()
	channel := channels[0]
	makeVersion(t, root, "1.0.998", channel.Binary, true)
	makeVersion(t, root, "1.0.9260", channel.Binary, true)
	makeVersion(t, root, "1.0.9261", channel.Binary, false)

	got, err := currentExe(root, channel)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "app-1.0.9260", channel.Binary)
	if got != want {
		t.Fatalf("esperava %s, veio %s", want, got)
	}
}

func TestCurrentExeWithoutCompleteVersion(t *testing.T) {
	root := t.TempDir()
	makeVersion(t, root, "1.0.1", channels[0].Binary, false)
	if _, err := currentExe(root, channels[0]); err == nil {
		t.Fatal("aceitou uma versao incompleta")
	}
}
