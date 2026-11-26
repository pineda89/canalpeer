package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"canalpeer.com/core/route"
)

// Silent self-update: a newer release is downloaded and staged while the app
// runs, and swapped in when the window closes, never mid-match. Anything that
// goes wrong leaves the installed version untouched.

// stageUpdate extracts a release (a zip, a tar.gz on Linux) into dest and
// returns what replaces the installed app: dest itself on Windows,
// dest/CanalPeer.app on macOS, dest/CanalPeer on Linux.
func stageUpdate(archive, dest, goos string) (string, error) {
	if goos == "linux" {
		if err := extractTarGz(archive, dest); err != nil {
			return "", err
		}
		root := filepath.Join(dest, "CanalPeer")
		if _, err := os.Stat(filepath.Join(root, "canalpeer")); err != nil {
			return "", errors.New("update is missing the app")
		}
		return root, nil
	}
	r, err := zip.OpenReader(archive)
	if err != nil {
		return "", err
	}
	defer r.Close()
	for _, f := range r.File {
		// macOS metadata (AppleDouble "._x", __MACOSX/) is not part of the app,
		// and inside a bundle it breaks the code signature.
		if strings.HasPrefix(f.Name, "__MACOSX/") || strings.HasPrefix(filepath.Base(f.Name), "._") {
			continue
		}
		name, err := safeName(f.Name)
		if err != nil {
			return "", err
		}
		if err := extract(f, filepath.Join(dest, name)); err != nil {
			return "", err
		}
	}
	root, want := dest, filepath.Join(dest, "CanalPeer.exe")
	if goos == "darwin" {
		root = filepath.Join(dest, "CanalPeer.app")
		want = filepath.Join(root, "Contents", "MacOS", "canalpeer")
	}
	if _, err := os.Stat(want); err != nil {
		return "", errors.New("update is missing the app")
	}
	return root, nil
}

// safeName is an archive entry's path, refused if it would land outside the destination.
func safeName(entry string) (string, error) {
	name := filepath.Clean(filepath.FromSlash(entry))
	if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path in update: %q", entry)
	}
	return name, nil
}

// extractTarGz unpacks the Linux release: plain files and folders only.
func extractTarGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name, err := safeName(h.Name)
		if err != nil {
			return err
		}
		path := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(path, 0o755)
		case tar.TypeReg:
			err = writeFile(path, tr, os.FileMode(h.Mode).Perm())
		default:
			err = fmt.Errorf("unexpected entry in update: %q", h.Name)
		}
		if err != nil {
			return err
		}
	}
}

func writeFile(path string, r io.Reader, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func extract(f *zip.File, path string) error {
	mode := f.Mode()
	if mode.IsDir() {
		return os.MkdirAll(path, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	if mode&os.ModeSymlink != 0 { // frameworks inside the macOS player use them
		target, err := io.ReadAll(rc)
		if err != nil {
			return err
		}
		// A link out of its own folder would let a later entry write through
		// it, outside the staging folder.
		if !filepath.IsLocal(filepath.FromSlash(string(target))) {
			return fmt.Errorf("symlink out of its folder in update: %q", f.Name)
		}
		return os.Symlink(string(target), path)
	}
	return writeFile(path, rc, mode.Perm())
}

// exeName is the app's executable inside its install folder (Windows, Linux).
func exeName(goos string) string {
	if goos == "windows" {
		return "CanalPeer.exe"
	}
	return "canalpeer"
}

// applyExeUpdate copies the staged files over the install of the running exe,
// whatever it is called (the Windows download keeps its versioned name): the
// staged executable lands on exe itself. The exe that is still running can be
// renamed but not overwritten, so it moves aside first.
func applyExeUpdate(staged, exe, goos string) error {
	install := filepath.Dir(exe)
	os.Remove(exe + ".old")
	if err := os.Rename(exe, exe+".old"); err != nil {
		return err
	}
	err := filepath.Walk(staged, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(staged, p)
		dst := filepath.Join(install, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if rel == exeName(goos) {
			dst = exe
		}
		return copyFile(p, dst, info.Mode())
	})
	if err != nil {
		os.Rename(exe+".old", exe) // keep a working install
	}
	return err
}

func cleanupOldExe(exe string) { os.Remove(exe + ".old") }

// applyMacUpdate swaps the whole bundle; macOS lets a running app's bundle move.
func applyMacUpdate(staged, installed, trash string) error {
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return err
	}
	old := filepath.Join(trash, fmt.Sprintf("CanalPeer-%d.app", time.Now().UnixNano()))
	if err := os.Rename(installed, old); err != nil {
		return err
	}
	if err := os.Rename(staged, installed); err != nil {
		os.Rename(old, installed)
		return err
	}
	os.RemoveAll(old)
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeFile(dst, in, mode.Perm())
}

func download(url, path string) error {
	c := route.HTTPClient(10 * time.Minute)
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", resp.Status)
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// staged is an update ready to swap in when the app closes.
type staged struct{ version, root string }

// selfUpdateTarget is what an update replaces (the bundle on macOS, the
// running executable elsewhere), or ok=false when the app runs from somewhere
// it cannot rewrite (a read-only or translocated location) or has no key to
// verify a release with: then the viewer is told about the new version instead.
func selfUpdateTarget(goos string) (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if updateKey == "" {
		return "", false
	}
	exe, _ = filepath.EvalSymlinks(exe)
	target, parent := exe, filepath.Dir(exe)
	if goos == "darwin" {
		target = filepath.Clean(filepath.Join(filepath.Dir(exe), "..", ".."))
		parent = filepath.Dir(target)
		if !strings.HasSuffix(target, ".app") || strings.Contains(target, "/AppTranslocation/") {
			return "", false
		}
	}
	probe := filepath.Join(parent, ".canalpeer-write-test")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return "", false
	}
	os.Remove(probe)
	return target, true
}

// prepareUpdate downloads a release from the first source that serves it
// with a good signature and stages it under dir.
func prepareUpdate(version string, urls []string, sig, dir, goos string) (*staged, error) {
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	zipPath := filepath.Join(dir, "release") // a zip, or a tar.gz on Linux
	var err error
	for _, u := range urls {
		if err = download(u, zipPath); err == nil {
			if err = verifyRelease(zipPath, sig); err == nil {
				break
			}
		}
	}
	if err != nil {
		return nil, err
	}
	root, err := stageUpdate(zipPath, filepath.Join(dir, "staged"), goos)
	if err != nil {
		return nil, err
	}
	return &staged{version, root}, nil
}

// applyUpdate swaps the staged release in; called once the window has closed.
func applyUpdate(s *staged, target, dataDir, goos string) error {
	var err error
	if goos != "darwin" {
		err = applyExeUpdate(s.root, target, goos)
	} else {
		err = applyMacUpdate(s.root, target, filepath.Join(dataDir, "update", "old"))
	}
	if err != nil {
		os.WriteFile(filepath.Join(dataDir, "update-failed"), []byte(s.version), 0o644)
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "updated-to"), []byte(s.version), 0o644)
}

// applyFailed reports whether swapping this version in failed before (an
// install folder the user cannot write, say). It is then offered as a download
// instead of failing the same way at every close without a word.
func applyFailed(dataDir, version string) bool {
	b, err := os.ReadFile(filepath.Join(dataDir, "update-failed"))
	return err == nil && string(b) == version
}
