package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightRejectsMissingPlayer(t *testing.T) {
	err := preflight(filepath.Join(t.TempDir(), "mpv", "mpv.exe"))
	if err == nil || !strings.Contains(err.Error(), "Extract") {
		t.Fatalf("got %v", err)
	}
}

func TestPreflightAcceptsPresentPlayer(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "mpv")
	os.WriteFile(bin, []byte("x"), 0o755)
	if err := preflight(bin); err != nil {
		t.Fatal(err)
	}
}
