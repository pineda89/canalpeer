package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ensureMpv puts the player the Windows exe carries (src: VERSION, SHA256SUMS
// and a .gz per file) in root/<version>/ and returns its mpv.exe. Every start
// checks the files against the sums and rewrites what is missing or altered,
// so a cut-short first run or a meddling antivirus never leaves a broken player.
func ensureMpv(src fs.FS, root string) (string, error) {
	v, err := fs.ReadFile(src, "VERSION")
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(v))
	sums, err := fs.ReadFile(src, "SHA256SUMS")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		want, name, ok := strings.Cut(line, "  ")
		if !ok || name != filepath.Base(name) {
			return "", fmt.Errorf("bad SHA256SUMS line %q", line)
		}
		path := filepath.Join(dir, name)
		if b, err := os.ReadFile(path); err == nil && hash(b) == want {
			continue
		}
		if err := writeEmbedded(src, name, want, path); err != nil {
			return "", err
		}
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() && e.Name() != version {
			os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
	return filepath.Join(dir, "mpv.exe"), nil
}

func writeEmbedded(src fs.FS, name, want, path string) error {
	f, err := src.Open(name + ".gz")
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if hash(b) != want {
		return fmt.Errorf("the embedded %s does not match its sum", name)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// removeLegacyMpv deletes the mpv folder 1.0.x kept next to the exe, once an
// update has replaced that exe with one that carries its own player.
// Only a folder holding exactly what 1.0.x put there: anything else may be the
// viewer's own mpv.
func removeLegacyMpv(installDir string) {
	dir := filepath.Join(installDir, "mpv")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	hasMpv := false
	for _, e := range entries {
		switch e.Name() {
		case "mpv.exe":
			hasMpv = true
		case "vulkan-1.dll":
		default:
			return
		}
	}
	if hasMpv {
		os.RemoveAll(dir)
	}
}
