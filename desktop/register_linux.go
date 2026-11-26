package main

import (
	_ "embed"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed linux/canalpeer.svg
var iconSVG []byte

const desktopID = "canalpeer.desktop"

// desktopEntry is the menu entry that also opens acestream:// links; Exec
// follows the binary wherever the viewer extracted it.
func desktopEntry(exe string) string {
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`).Replace(exe) + `"`
	return "[Desktop Entry]\nType=Application\nName=CanalPeer\nComment=Watch AceStream channels\n" +
		"Exec=" + quoted + " %u\nIcon=canalpeer\nTerminal=false\nStartupWMClass=canalpeer\n" +
		"Categories=AudioVideo;Video;Player;\nMimeType=x-scheme-handler/acestream;\n"
}

// ensureRegistered puts CanalPeer in the applications menu and makes it open
// acestream:// links for this user (no root), asking first if another app has them.
func ensureRegistered(exe, dataDir string) {
	share := os.Getenv("XDG_DATA_HOME")
	if share == "" {
		home, _ := os.UserHomeDir()
		share = filepath.Join(home, ".local", "share")
	}
	apps, icons := filepath.Join(share, "applications"), filepath.Join(share, "icons", "hicolor", "scalable", "apps")
	for path, b := range map[string][]byte{
		filepath.Join(apps, desktopID):        []byte(desktopEntry(exe)),
		filepath.Join(icons, "canalpeer.svg"): iconSVG,
	} {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			log.Print("register: ", err)
			return
		}
	}
	exec.Command("update-desktop-database", apps).Run() // optional: menus also rescan alone

	if _, err := exec.LookPath("xdg-mime"); err != nil {
		log.Print("register acestream://: no xdg-mime")
		return
	}
	out, _ := exec.Command("xdg-mime", "query", "default", "x-scheme-handler/acestream").Output()
	current := strings.TrimSpace(string(out))
	if current == desktopID {
		current = exe // ours: registrationDecision then leaves it alone
	}
	s := loadSettings(dataDir)
	switch registrationDecision(current, exe, s.HandleLinks) {
	case regNothing:
		return
	case regAsk:
		yes, asked := askYesNo("Open acestream:// links with CanalPeer?")
		if !asked {
			return // no way to ask here: leave the other app, ask next time
		}
		s.HandleLinks = &yes
		saveSettings(dataDir, s)
		if !yes {
			return
		}
	}
	if err := exec.Command("xdg-mime", "default", desktopID, "x-scheme-handler/acestream").Run(); err != nil {
		log.Print("register acestream://: ", err)
	}
}

// askYesNo asks with whichever dialog tool the desktop has; asked is false when there is none.
func askYesNo(text string) (yes, asked bool) {
	for _, c := range [][]string{
		{"zenity", "--question", "--title=CanalPeer", "--text=" + text},
		{"kdialog", "--title", "CanalPeer", "--yesno", text},
	} {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		err := exec.Command(c[0], c[1:]...).Run()
		if _, isExit := err.(*exec.ExitError); err != nil && !isExit {
			continue
		}
		return err == nil, true
	}
	return false, false
}

// alert tells the user something went wrong before any window exists.
func alert(text string) {
	for _, c := range [][]string{
		{"zenity", "--error", "--title=CanalPeer", "--no-markup", "--text=" + text},
		{"kdialog", "--title", "CanalPeer", "--error", text},
		{"notify-send", "--urgency=critical", "CanalPeer", text},
	} {
		if exec.Command(c[0], c[1:]...).Run() == nil {
			return
		}
	}
	os.Stderr.WriteString(text + "\n") // started from a terminal, at least
}
