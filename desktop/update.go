package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"time"

	"canalpeer.com/core/route"
)

// Where the current desktop version is announced: the site and the public
// repo, asked at once, so blocking canalpeer.com does not stop updates.
var releaseURLs = []string{
	"https://canalpeer.com/app/desktop.json",
	"https://github.com/pineda89/canalpeer/releases/latest/download/desktop.json",
}

var semver = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

// versionCode orders "major.minor.patch" (minor and patch below 100); 0 if malformed.
func versionCode(v string) int {
	m := semver.FindStringSubmatch(v)
	if m == nil {
		return 0
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	c, _ := strconv.Atoi(m[3])
	return a*10000 + b*100 + c
}

// updatePlatform is what desktop.json calls this build: the OS, plus the
// architecture on Linux, the only one released for two.
func updatePlatform(goos, goarch string) string {
	if goos == "linux" {
		return map[string]string{"amd64": "linux-x64", "arm64": "linux-arm64"}[goarch]
	}
	return goos
}

func parseDesktopRelease(body []byte, platform string) (version, url string, ok bool) {
	var r struct {
		Version, Windows, Mac string
		LinuxX64              string `json:"linuxX64"`
		LinuxArm64            string `json:"linuxArm64"`
	}
	if json.Unmarshal(body, &r) != nil || versionCode(r.Version) == 0 {
		return "", "", false
	}
	url = map[string]string{"windows": r.Windows, "darwin": r.Mac, "linux-x64": r.LinuxX64, "linux-arm64": r.LinuxArm64}[platform]
	// https only; plain http only to this machine, for testing the update path.
	if u, err := neturl.Parse(url); err != nil || u.Scheme != "https" && (u.Scheme != "http" || u.Hostname() != "127.0.0.1") {
		return "", "", false
	}
	return r.Version, url, true
}

// updateKey is the public half of the key that signs releases (base64; made
// by core/cmd/sign gen). While it is empty nothing can be verified, so the
// app never updates itself and only tells the viewer about a new version.
var updateKey = "3887HzifcYRQOKmyoVH+xolrghO7vTXMZIQIA10Ehpg="

// releaseSig is the signature a source gives for version's download on this
// platform ("sig" in desktop.json, by the download's own name), "" if none does.
func releaseSig(bodies [][]byte, platform, version string) string {
	name := map[string]string{"windows": "windows", "darwin": "mac", "linux-x64": "linuxX64", "linux-arm64": "linuxArm64"}[platform]
	for _, b := range bodies {
		var r struct {
			Version string
			Sig     map[string]string
		}
		if json.Unmarshal(b, &r) == nil && r.Version == version && r.Sig[name] != "" {
			return r.Sig[name]
		}
	}
	return ""
}

// verifyRelease checks a downloaded release against its signature: ed25519
// over the file's SHA-256, by updateKey. Whoever can write desktop.json can
// point every install at any file; only this says the file is ours.
func verifyRelease(path, sig string) error {
	key, err := base64.StdEncoding.DecodeString(updateKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return errors.New("update: this build has no key to verify releases with")
	}
	s, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return errors.New("update: malformed signature")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !ed25519.Verify(key, h.Sum(nil), s) {
		return errors.New("update: the download is not signed by CanalPeer")
	}
	return nil
}

// pickRelease takes the newest version any source announces, with the
// download of every source that announces it, in the sources' order.
func pickRelease(bodies [][]byte, platform string) (version string, urls []string, ok bool) {
	for _, b := range bodies {
		v, u, ok := parseDesktopRelease(b, platform)
		switch {
		case !ok:
		case versionCode(v) > versionCode(version):
			version, urls = v, []string{u}
		case v == version:
			urls = append(urls, u)
		}
	}
	return version, urls, version != ""
}

// checkForUpdate returns a newer release for this OS, where to download it
// and its signature, or ok=false (no network bothers nobody).
func checkForUpdate(current string) (version string, urls []string, sig string, ok bool) {
	c := route.HTTPClient(15 * time.Second) // through WARP when the viewer chose it
	sources := releaseURLs
	if v := os.Getenv("CANALPEER_RELEASE_URL"); v != "" { // for testing the update path
		sources = []string{v}
	}
	bodies := make([][]byte, len(sources))
	var wg sync.WaitGroup
	for i, u := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.Get(u)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				bodies[i], _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			}
		}()
	}
	wg.Wait()
	platform := updatePlatform(runtime.GOOS, runtime.GOARCH)
	version, urls, ok = pickRelease(bodies, platform)
	if !ok || versionCode(version) <= versionCode(current) {
		return "", nil, "", false
	}
	return version, urls, releaseSig(bodies, platform, version), true
}

func openInBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	}
	return exec.Command("open", url).Start()
}
