package main

import "testing"

const release = `{"version":"1.2.3",
 "windows":"https://dl.canalpeer.com/CanalPeer-1.2.3-windows-x64.zip",
 "mac":"https://dl.canalpeer.com/CanalPeer-1.2.3-mac.zip",
 "linuxX64":"https://dl.canalpeer.com/CanalPeer-1.2.3-linux-x64.tar.gz",
 "linuxArm64":"https://dl.canalpeer.com/CanalPeer-1.2.3-linux-arm64.tar.gz"}`

func TestParseDesktopReleasePicksTheOS(t *testing.T) {
	for goos, want := range map[string]string{
		"windows":                        "https://dl.canalpeer.com/CanalPeer-1.2.3-windows-x64.zip",
		"darwin":                         "https://dl.canalpeer.com/CanalPeer-1.2.3-mac.zip",
		updatePlatform("linux", "amd64"): "https://dl.canalpeer.com/CanalPeer-1.2.3-linux-x64.tar.gz",
		updatePlatform("linux", "arm64"): "https://dl.canalpeer.com/CanalPeer-1.2.3-linux-arm64.tar.gz",
	} {
		v, u, ok := parseDesktopRelease([]byte(release), goos)
		if !ok || v != "1.2.3" || u != want {
			t.Errorf("%s: %q %q %v", goos, v, u, ok)
		}
	}
	// A desktop.json from before Linux builds offers Linux nothing; nor does an unknown architecture.
	before := `{"version":"1.2.3","windows":"https://x/a.zip","mac":"https://x/b.zip"}`
	if _, _, ok := parseDesktopRelease([]byte(before), "linux-x64"); ok {
		t.Error("linux offered a build that does not exist")
	}
	if _, _, ok := parseDesktopRelease([]byte(release), updatePlatform("linux", "riscv64")); ok {
		t.Error("riscv64 offered another architecture's build")
	}
}

func TestParseRejectsNonHTTPSAndHTML(t *testing.T) {
	for _, body := range []string{
		"<!doctype html><title>CanalPeer</title>",
		`{"version":"1.2.3","windows":"http://x/a.zip","mac":"http://x/b.zip"}`,
		`{"version":"1.2.3","mac":"http://127.0.0.1:80@evil.example/b.zip"}`, // userinfo, not this machine
		`{"version":"1.2","windows":"https://x/a.zip","mac":"https://x/b.zip"}`,
	} {
		if _, _, ok := parseDesktopRelease([]byte(body), "darwin"); ok {
			t.Errorf("accepted %q", body)
		}
	}
}

func TestVersionCode(t *testing.T) {
	if got := versionCode("1.2.3"); got != 10203 {
		t.Fatalf("got %d", got)
	}
	if got := versionCode("bad"); got != 0 {
		t.Fatalf("bad version: %d", got)
	}
}

func TestPickReleaseTakesTheNewestAndKeepsEveryURL(t *testing.T) {
	web := []byte(`{"version":"1.2.3","mac":"https://canalpeer.com/dl/CanalPeer-1.2.3-mac.zip"}`)
	gh := []byte(`{"version":"1.2.3","mac":"https://github.com/pineda89/canalpeer/releases/download/v1.2.3/CanalPeer-1.2.3-mac.zip"}`)
	older := []byte(`{"version":"1.2.2","mac":"https://x/old.zip"}`)
	v, urls, ok := pickRelease([][]byte{nil, older, web, gh}, "darwin")
	if !ok || v != "1.2.3" || len(urls) != 2 || urls[0] != "https://canalpeer.com/dl/CanalPeer-1.2.3-mac.zip" {
		t.Fatalf("%q %q %v", v, urls, ok)
	}
	// The web blocked: the repo alone is enough.
	if v, urls, ok := pickRelease([][]byte{nil, gh}, "darwin"); !ok || v != "1.2.3" || len(urls) != 1 {
		t.Fatalf("repo only: %q %q %v", v, urls, ok)
	}
	if _, _, ok := pickRelease([][]byte{nil, nil}, "darwin"); ok {
		t.Fatal("nothing reachable gave a release")
	}
}

func TestReleaseSigComesFromAnySourceOfThatVersion(t *testing.T) {
	web := []byte(`{"version":"1.2.3","mac":"https://x/b.zip"}`) // not signed yet
	gh := []byte(`{"version":"1.2.3","mac":"https://y/b.zip","sig":{"mac":"c2ln","linuxX64":"bGludXg="}}`)
	older := []byte(`{"version":"1.2.2","mac":"https://x/old.zip","sig":{"mac":"b2xk"}}`)
	if got := releaseSig([][]byte{nil, older, web, gh}, "darwin", "1.2.3"); got != "c2ln" {
		t.Fatalf("got %q", got)
	}
	if got := releaseSig([][]byte{gh}, "linux-x64", "1.2.3"); got != "bGludXg=" {
		t.Fatalf("linux: %q", got)
	}
	if got := releaseSig([][]byte{web, older}, "darwin", "1.2.3"); got != "" {
		t.Fatalf("another version's signature: %q", got)
	}
}
