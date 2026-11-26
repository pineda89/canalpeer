package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	yes := true
	if err := saveSettings(dir, settings{Vpn: "on", HandleLinks: &yes}); err != nil {
		t.Fatal(err)
	}
	if s := loadSettings(dir); s.Vpn != "on" || s.HandleLinks == nil || !*s.HandleLinks {
		t.Fatalf("loaded %+v", s)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestSettingsDefaultAuto(t *testing.T) {
	dir := t.TempDir()
	if s := loadSettings(dir); s.Vpn != "auto" {
		t.Fatalf("missing file: %+v", s)
	}
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{not json"), 0o644)
	if s := loadSettings(dir); s.Vpn != "auto" {
		t.Fatalf("corrupt file: %+v", s)
	}
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"vpn":"sideways"}`), 0o644)
	if s := loadSettings(dir); s.Vpn != "auto" {
		t.Fatalf("unknown mode: %+v", s)
	}
}
