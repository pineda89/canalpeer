package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// embedded builds what the Windows exe carries: gzipped files, their sums, the version.
func embedded(files map[string][]byte) fstest.MapFS {
	fsys := fstest.MapFS{"VERSION": {Data: []byte("v0.41.0\n")}}
	sums := ""
	for name, b := range files {
		fsys[name+".gz"] = &fstest.MapFile{Data: gz(b)}
		sums += sum(b) + "  " + name + "\n"
	}
	fsys["SHA256SUMS"] = &fstest.MapFile{Data: []byte(sums)}
	return fsys
}

var mpvPayload = map[string][]byte{"mpv.exe": []byte("MZ the player"), "vulkan-1.dll": []byte("MZ vulkan")}

func TestEnsureMpvFresh(t *testing.T) {
	root := t.TempDir()
	exe, err := ensureMpv(embedded(mpvPayload), root)
	if err != nil {
		t.Fatal(err)
	}
	if exe != filepath.Join(root, "v0.41.0", "mpv.exe") {
		t.Fatalf("exe %q", exe)
	}
	for name, want := range mpvPayload {
		if b, _ := os.ReadFile(filepath.Join(root, "v0.41.0", name)); !bytes.Equal(b, want) {
			t.Fatalf("%s = %q", name, b)
		}
	}
}

func TestEnsureMpvRepairsTampered(t *testing.T) {
	root := t.TempDir()
	src := embedded(mpvPayload)
	exe, _ := ensureMpv(src, root)
	os.WriteFile(exe, []byte("MZ"), 0o755) // cut short: a full disk, or an antivirus
	if _, err := ensureMpv(src, root); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); !bytes.Equal(b, mpvPayload["mpv.exe"]) {
		t.Fatalf("not repaired: %q", b)
	}
}

func TestEnsureMpvDropsOldVersions(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "v0.40.0"), 0o755)
	if _, err := ensureMpv(embedded(mpvPayload), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "v0.40.0")); !os.IsNotExist(err) {
		t.Fatal("old mpv version left behind")
	}
}

func TestEnsureMpvBadEmbed(t *testing.T) {
	src := embedded(mpvPayload)
	src["SHA256SUMS"] = &fstest.MapFile{Data: []byte(sum([]byte("other")) + "  mpv.exe\n")}
	if exe, err := ensureMpv(src, t.TempDir()); err == nil || exe != "" {
		t.Fatalf("a bad embed was accepted: %q %v", exe, err)
	}
}

func TestRemoveLegacyMpv(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "mpv"), 0o755)
	os.WriteFile(filepath.Join(dir, "mpv", "notes.txt"), nil, 0o644)
	removeLegacyMpv(dir)
	if _, err := os.Stat(filepath.Join(dir, "mpv")); err != nil {
		t.Fatal("a folder without mpv.exe was removed")
	}
	legacy := t.TempDir() // exactly what the 1.0.x zip put next to the exe
	os.MkdirAll(filepath.Join(legacy, "mpv"), 0o755)
	os.WriteFile(filepath.Join(legacy, "mpv", "mpv.exe"), nil, 0o644)
	os.WriteFile(filepath.Join(legacy, "mpv", "vulkan-1.dll"), nil, 0o644)
	removeLegacyMpv(legacy)
	if _, err := os.Stat(filepath.Join(legacy, "mpv")); !os.IsNotExist(err) {
		t.Fatal("the 1.0.x mpv folder is still there")
	}
}

// Someone's own mpv next to the exe (more than 1.0.x put there) is theirs: keep it.
func TestRemoveLegacyMpvKeepsOtherMpv(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "mpv", "portable_config"), 0o755)
	for _, f := range []string{"mpv.exe", "vulkan-1.dll", "mpv.com"} {
		os.WriteFile(filepath.Join(dir, "mpv", f), nil, 0o644)
	}
	removeLegacyMpv(dir)
	if _, err := os.Stat(filepath.Join(dir, "mpv", "mpv.exe")); err != nil {
		t.Fatal("a mpv folder that is not CanalPeer's was removed")
	}
}
