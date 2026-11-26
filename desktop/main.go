// Command canalpeer is the CanalPeer desktop player: the shared core joins the
// swarm on this machine and a bundled mpv shows the stream.
package main

import (
	"canalpeer.com/core/changelog"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"canalpeer.com/core/mobile"
)

// version is the repo's VERSION, shared by every app; set at build with
// -ldflags "-X main.version=…" (scripts/package-desktop.sh).
var version = "0.0.0"

func main() {
	runMain(func() { os.Exit(app()) })
}

func app() int {
	// Windows starts a new process per clicked link: hand it to the open window.
	instance := instancePath()
	handed, primary, err := claimInstance(instance)
	if err == nil && !primary {
		if l := linkArg(); l != "" {
			sendToPrimary(instance, l)
		}
		return 0
	}
	data := dataDir()
	bin, err := mpvBinary()
	if err != nil {
		alert("CanalPeer could not prepare its player.\n\n" + err.Error())
		return 1
	}
	if err := preflight(bin); err != nil {
		alert(err.Error())
		return 1
	}
	if exe, err := os.Executable(); err == nil {
		ensureRegistered(exe, data)
	}
	logf, _ := os.OpenFile(filepath.Join(data, "canalpeer.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	log.SetOutput(logf)

	conf, script := filepath.Join(data, "input.conf"), filepath.Join(data, "controls.lua")
	for path, b := range map[string][]byte{conf: inputConf, script: controlsLua} {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			log.Print(err)
			return 1
		}
	}
	// The route first: the update check and Sync already go through WARP when it is on.
	settings := loadSettings(data)
	mobile.SetVpn(settings.Vpn, data)
	if version != "0.0.0" { // a development build is not an install
		mobile.ReportOpen(map[string]string{"darwin": "mac", "windows": "windows", "linux": "linux"}[runtime.GOOS], version, settings.Vpn, data)
	}
	mobile.Sync(data)
	up := &appUpdate{}

	// Silent self-update: download and stage now, swap in when the app ends.
	target, canUpdate := selfUpdateTarget(runtime.GOOS)
	if canUpdate && runtime.GOOS != "darwin" {
		cleanupOldExe(target)
	}
	if runtime.GOOS == "windows" {
		if exe, err := os.Executable(); err == nil {
			removeLegacyMpv(filepath.Dir(exe)) // 1.0.x kept mpv next to the exe; this one carries it
		}
	}
	var notes []string // what changed, on the launcher after updating itself; the UI is English
	if b, err := os.ReadFile(filepath.Join(data, "updated-to")); err == nil {
		os.Remove(filepath.Join(data, "updated-to"))
		up.set(offer{text: "Updated to CanalPeer " + strings.TrimSpace(string(b))})
		notes = changelog.Notes(version, "en", "desktop")
	}
	go func() {
		v, urls, sig, ok := checkForUpdate(version)
		if !ok {
			return
		}
		if !canUpdate || applyFailed(data, v) {
			up.set(offer{fmt.Sprintf("CanalPeer %s is available — press U to download", v), urls[0], v})
			return
		}
		s, err := prepareUpdate(v, urls, sig, filepath.Join(data, "update", "next"), runtime.GOOS)
		if err != nil {
			log.Print("update: ", err)
			return
		}
		up.stage(s)
	}()

	links := make(chan string, 8)
	for _, ch := range []<-chan string{urlEvents(), handed} {
		go func() {
			for u := range ch {
				links <- u
			}
		}()
	}
	first := linkArg()
	if runtime.GOOS == "darwin" {
		// A link that started the app arrives as an Apple Event just after launch:
		// wait for it, so the launcher is not shown on the way to the player.
		select {
		case first = <-links:
		case <-time.After(700 * time.Millisecond):
		}
	}
	play := func(link string, links <-chan string) ending {
		return playOnce(bin, link, links, conf, script, data, version, up)
	}
	var launch launchFunc
	if open := webViewOpener(data); open != nil {
		view := func() launcherView {
			text, url := up.banner()
			v := launcherView{Version: version, Banner: text}
			if url == "" && up.staged() == nil { // the "Updated to" line, not a newer release
				v.Notes = notes
			}
			if url != "" {
				v.BannerAction = "download"
			}
			v.Recent = recentRows(mobile.History(data), time.Now())
			v.Vpn, v.VpnInUse = loadSettings(data).Vpn, mobile.VpnInUse()
			return v
		}
		download := func() {
			if _, url := up.banner(); url != "" {
				openInBrowser(url)
			}
		}
		// The notice ships next to the player (Windows) or in the bundle's Resources (macOS).
		notices := filepath.Join(filepath.Dir(bin), "THIRD-PARTY-NOTICES.txt")
		if runtime.GOOS == "darwin" {
			notices = filepath.Join(filepath.Dir(bin), "..", "..", "..", "THIRD-PARTY-NOTICES.txt")
		}
		if exe, err := os.Executable(); err == nil && runtime.GOOS == "linux" { // mpv is the system's; the notice is in the tarball
			notices = filepath.Join(filepath.Dir(exe), "THIRD-PARTY-NOTICES.txt")
		}
		forget := func(id string) { mobile.Forget(data, id) }
		setVpn := func(mode string) {
			saveVpn(data, mode)
			mobile.SetVpn(mode, data)
		}
		launch = makeLaunch(open, view, download, func() { openInBrowser(notices) }, forget, setVpn)
	}
	runFlow(first, links, launch, play)
	if s := up.staged(); s != nil {
		if err := applyUpdate(s, target, data, runtime.GOOS); err != nil {
			log.Print("update: ", err)
		}
	}
	return 0
}

// playOnce runs one mpv window for link until it ends, and says how it ended.
func playOnce(bin, link string, links <-chan string, conf, script, data, version string, up *appUpdate) ending {
	ipc := ipcPath()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // mpv must not outlive its viewing
	m, cmd, err := startMpv(ctx, bin, mpvArgs(ipc, conf, script), ipc)
	if err != nil {
		log.Print("mpv: ", err)
		alert("CanalPeer could not start its player.\n\n" + err.Error())
		return endQuit
	}
	release := killOnExit(func() { cmd.Process.Kill() })
	defer release()
	var p *player
	p = newPlayer(m, func(id string) (session, error) {
		return mobile.Start(&mobile.Options{ContentID: id, CacheDir: data, DataDir: data, Vpn: p.vpn})
	}, filepath.Join(data, "cuts.jsonl"))
	p.version = version
	p.vpn, p.settingsDir = loadSettings(data).Vpn, data
	p.setVpn = func(mode string) { mobile.SetVpn(mode, data) }
	up.attach(p)
	defer up.attach(nil)
	if link != "" {
		p.play(link)
	}
	done := make(chan struct{})
	go func() {
		p.run(ctx)
		close(done)
	}()
	// ponytail: a link arriving in the instant mpv exits is lost; clicking it again plays it.
	for running := true; running; {
		select {
		case l := <-links:
			p.play(l)
		case <-done:
			running = false
		}
	}
	m.close()
	cmd.Wait()
	if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == mpvBackCode {
		return endBack
	}
	return endQuit
}

func dataDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "CanalPeer")
	os.MkdirAll(dir, 0o755)
	return dir
}

func ipcPath() string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\canalpeer-mpv-%d`, os.Getpid())
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("canalpeer-mpv-%d.sock", os.Getpid()))
}

// mpvBinary is the mpv shipped inside the app (CANALPEER_MPV overrides it for
// development): inside the bundle on macOS, carried in the exe on Windows,
// the distribution's own on Linux.
func mpvBinary() (string, error) {
	if p := os.Getenv("CANALPEER_MPV"); p != "" {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		return embeddedMpv()
	}
	if runtime.GOOS == "linux" {
		if p, err := exec.LookPath("mpv"); err == nil {
			return p, nil
		}
		return "", errors.New("CanalPeer plays video with mpv, which is not installed.\n\nInstall it from your distribution, for example:\n\nsudo apt install mpv")
	}
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "..", "Resources", "CanalPeer.app", "Contents", "MacOS", "mpv"), nil
}

// linkArg is the acestream:// link Windows passes as an argument, if any;
// anything else on the command line is not ours to interpret.
func linkArg() string {
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(strings.ToLower(a), "acestream:") {
			return a
		}
	}
	return ""
}
