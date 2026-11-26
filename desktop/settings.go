package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// settings is settings.json in the data dir: what the viewer chose.
type settings struct {
	HandleLinks *bool  `json:"handleLinks,omitempty"` // Windows: take acestream:// from another app
	Vpn         string `json:"vpn"`                   // WARP: "auto", "on" or "off"
}

// loadSettings never fails: a missing or broken file is the defaults.
func loadSettings(dir string) settings {
	var s settings
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if json.Unmarshal(b, &s) != nil {
		s = settings{}
	}
	if s.Vpn != "on" && s.Vpn != "off" {
		s.Vpn = "auto"
	}
	return s
}

// saveVpn stores a new WARP mode, keeping the other settings.
func saveVpn(dir, mode string) {
	s := loadSettings(dir)
	s.Vpn = mode
	if err := saveSettings(dir, s); err != nil {
		log.Print("settings: ", err)
	}
}

// saveSettings replaces the file whole, so a crash mid-write leaves the old one.
func saveSettings(dir string, s settings) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	f, err := os.CreateTemp(dir, "settings.json.tmp*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(dir, "settings.json"))
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}
