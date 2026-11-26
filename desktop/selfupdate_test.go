package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "u.zip")
	f, _ := os.Create(path)
	w := zip.NewWriter(f)
	for name, body := range files {
		e, _ := w.Create(name)
		e.Write([]byte(body))
	}
	w.Close()
	f.Close()
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStageWindowsUpdate(t *testing.T) {
	z := writeZip(t, map[string]string{"CanalPeer.exe": "new", "mpv/mpv.exe": "mpv2"})
	root, err := stageUpdate(z, t.TempDir(), "windows")
	if err != nil || read(t, filepath.Join(root, "CanalPeer.exe")) != "new" {
		t.Fatalf("root %q err %v", root, err)
	}
}

func TestStageMacUpdate(t *testing.T) {
	z := writeZip(t, map[string]string{"CanalPeer.app/Contents/MacOS/canalpeer": "new"})
	root, err := stageUpdate(z, t.TempDir(), "darwin")
	if err != nil || filepath.Base(root) != "CanalPeer.app" {
		t.Fatalf("root %q err %v", root, err)
	}
}

// ditto keeps macOS metadata as "._name" entries; inside a bundle they break its signature.
func TestStageSkipsAppleDoubleFiles(t *testing.T) {
	z := writeZip(t, map[string]string{
		"CanalPeer.app/Contents/MacOS/canalpeer":   "new",
		"CanalPeer.app/Contents/MacOS/._canalpeer": "meta",
		"__MACOSX/CanalPeer.app/._Contents":        "meta",
	})
	dest := t.TempDir()
	if _, err := stageUpdate(z, dest, "darwin"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"CanalPeer.app/Contents/MacOS/._canalpeer", "__MACOSX"} {
		if _, err := os.Stat(filepath.Join(dest, p)); !os.IsNotExist(err) {
			t.Errorf("%s extracted", p)
		}
	}
}

func TestStageRejectsIncompleteOrHostileZips(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no exe":   {"mpv/mpv.exe": "x"},
		"zip slip": {"CanalPeer.exe": "x", "../../evil": "x"},
	} {
		if _, err := stageUpdate(writeZip(t, files), t.TempDir(), "windows"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestApplyWindowsUpdateKeepsTheRunningExeAside(t *testing.T) {
	install, staged := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(install, "CanalPeer.exe"), []byte("old"), 0o755)
	os.MkdirAll(filepath.Join(install, "mpv"), 0o755)
	os.WriteFile(filepath.Join(install, "mpv", "mpv.exe"), []byte("mpv1"), 0o755)
	os.WriteFile(filepath.Join(staged, "CanalPeer.exe"), []byte("new"), 0o755)
	os.MkdirAll(filepath.Join(staged, "mpv"), 0o755)
	os.WriteFile(filepath.Join(staged, "mpv", "mpv.exe"), []byte("mpv2"), 0o755)
	if err := applyExeUpdate(staged, filepath.Join(install, "CanalPeer.exe"), "windows"); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(install, "CanalPeer.exe")) != "new" ||
		read(t, filepath.Join(install, "CanalPeer.exe.old")) != "old" ||
		read(t, filepath.Join(install, "mpv", "mpv.exe")) != "mpv2" {
		t.Fatal("not applied")
	}
	cleanupOldExe(filepath.Join(install, "CanalPeer.exe"))
	if _, err := os.Stat(filepath.Join(install, "CanalPeer.exe.old")); !os.IsNotExist(err) {
		t.Fatal(".old left behind")
	}
}

func TestApplyMacUpdateSwapsTheBundle(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "Applications", "CanalPeer.app")
	staged := filepath.Join(dir, "staged", "CanalPeer.app")
	for p, body := range map[string]string{
		filepath.Join(installed, "Contents", "MacOS", "canalpeer"): "old",
		filepath.Join(staged, "Contents", "MacOS", "canalpeer"):    "new",
	} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o755)
	}
	if err := applyMacUpdate(staged, installed, filepath.Join(dir, "trash")); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(installed, "Contents", "MacOS", "canalpeer")) != "new" {
		t.Fatal("not swapped")
	}
}

// Against a real release zip: CANALPEER_MAC_ZIP=dist/CanalPeer-x-mac.zip go test -run TestStageRealMacZip
func TestStageRealMacZip(t *testing.T) {
	z := os.Getenv("CANALPEER_MAC_ZIP")
	if z == "" {
		t.Skip("set CANALPEER_MAC_ZIP")
	}
	dest := os.Getenv("CANALPEER_STAGE_DIR")
	root, err := stageUpdate(z, dest, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Contents/MacOS/canalpeer", "Contents/Resources/CanalPeer.app/Contents/MacOS/mpv"} {
		info, err := os.Stat(filepath.Join(root, p))
		if err != nil || info.Mode()&0o111 == 0 {
			t.Fatalf("%s not executable: %v %v", p, info, err)
		}
	}
}

func writeTarGz(t *testing.T, entries []tar.Header, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release")
	f, _ := os.Create(path)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(body))
		}
		tw.WriteHeader(&h)
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(body))
		}
	}
	tw.Close()
	gz.Close()
	f.Close()
	return path
}

func TestStageLinuxUpdateKeepsTheExecutableBit(t *testing.T) {
	z := writeTarGz(t, []tar.Header{
		{Name: "CanalPeer/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "CanalPeer/canalpeer", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "CanalPeer/THIRD-PARTY-NOTICES.txt", Typeflag: tar.TypeReg, Mode: 0o644},
	}, "x")
	dest := t.TempDir()
	root, err := stageUpdate(z, dest, "linux")
	if err != nil || root != filepath.Join(dest, "CanalPeer") {
		t.Fatal(root, err)
	}
	if fi, err := os.Stat(filepath.Join(root, "canalpeer")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatal("not executable", err)
	}
}

func TestStageLinuxUpdateRefusesBadArchives(t *testing.T) {
	for name, entries := range map[string][]tar.Header{
		"no app":  {{Name: "CanalPeer/THIRD-PARTY-NOTICES.txt", Typeflag: tar.TypeReg, Mode: 0o644}},
		"escapes": {{Name: "../canalpeer", Typeflag: tar.TypeReg, Mode: 0o755}},
		"symlink": {{Name: "CanalPeer/canalpeer", Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"}},
	} {
		if _, err := stageUpdate(writeTarGz(t, entries, "x"), t.TempDir(), "linux"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestApplyLinuxUpdateReplacesTheBinary(t *testing.T) {
	install, staged := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(install, "canalpeer"), []byte("old"), 0o755)
	os.WriteFile(filepath.Join(staged, "canalpeer"), []byte("new"), 0o755)
	if err := applyExeUpdate(staged, filepath.Join(install, "canalpeer"), "linux"); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(install, "canalpeer")) != "new" {
		t.Fatal("not applied")
	}
	cleanupOldExe(filepath.Join(install, "canalpeer"))
	if _, err := os.Stat(filepath.Join(install, "canalpeer.old")); !os.IsNotExist(err) {
		t.Fatal(".old left behind")
	}
}

// The Windows download keeps its versioned name: the update replaces that
// file, not a CanalPeer.exe that is not there.
func TestApplyWindowsUpdateToAnExeWithItsDownloadName(t *testing.T) {
	install, staged := t.TempDir(), t.TempDir()
	exe := filepath.Join(install, "CanalPeer-1.6.1-windows-x64.exe")
	os.WriteFile(exe, []byte("old"), 0o755)
	os.WriteFile(filepath.Join(staged, "CanalPeer.exe"), []byte("new"), 0o755)
	if err := applyExeUpdate(staged, exe, "windows"); err != nil {
		t.Fatal(err)
	}
	if read(t, exe) != "new" || read(t, exe+".old") != "old" {
		t.Fatal("not applied to the running exe")
	}
	if _, err := os.Stat(filepath.Join(install, "CanalPeer.exe")); !os.IsNotExist(err) {
		t.Fatal("left a second exe beside the running one")
	}
}

// A link out of its folder would let the next entry write outside staging.
func TestStageRefusesSymlinkOutOfItsFolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.zip")
	f, _ := os.Create(path)
	w := zip.NewWriter(f)
	h := &zip.FileHeader{Name: "CanalPeer.app/x"}
	h.SetMode(os.ModeSymlink | 0o777)
	e, _ := w.CreateHeader(h)
	e.Write([]byte("../../.."))
	e, _ = w.Create("CanalPeer.app/Contents/MacOS/canalpeer")
	e.Write([]byte("bin"))
	w.Close()
	f.Close()
	if _, err := stageUpdate(path, t.TempDir(), "darwin"); err == nil {
		t.Fatal("accepted a symlink leaving the bundle")
	}
}

func TestVerifyRelease(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	old := updateKey
	t.Cleanup(func() { updateKey = old })
	file := filepath.Join(t.TempDir(), "release")
	os.WriteFile(file, []byte("the release"), 0o600)
	sum := sha256.Sum256([]byte("the release"))
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sum[:]))

	updateKey = ""
	if verifyRelease(file, sig) == nil {
		t.Fatal("a build without a key verified a release")
	}
	updateKey = base64.StdEncoding.EncodeToString(pub)
	if err := verifyRelease(file, sig); err != nil {
		t.Fatal(err)
	}
	if verifyRelease(file, "") == nil || verifyRelease(file, "not base64!") == nil {
		t.Fatal("accepted a release without a signature")
	}
	os.WriteFile(file, []byte("another file"), 0o600)
	if verifyRelease(file, sig) == nil {
		t.Fatal("accepted a file the signature is not of")
	}
}

// A release that could not be swapped in is remembered, so it is offered as a
// download next time; a newer one gets its own try.
func TestFailedApplyIsRemembered(t *testing.T) {
	data := t.TempDir()
	missing := filepath.Join(t.TempDir(), "nothing-staged")
	if err := applyUpdate(&staged{"1.2.3", missing}, filepath.Join(t.TempDir(), "canalpeer"), data, "linux"); err == nil {
		t.Fatal("applied a release that is not there")
	}
	if !applyFailed(data, "1.2.3") || applyFailed(data, "1.2.4") {
		t.Fatalf("failed 1.2.3: %v, 1.2.4: %v", applyFailed(data, "1.2.3"), applyFailed(data, "1.2.4"))
	}
}
