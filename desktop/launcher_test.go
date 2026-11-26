package main

import (
	"strings"
	"testing"
)

func TestLauncherHTML(t *testing.T) {
	page := launcherHTML(launcherView{Version: "1.1.0"})
	for _, want := range []string{"Content ID or acestream:// link", ">Watch<", "Version 1.1.0",
		"P2P video player", "#5fb8a0", "@font-face", "Licenses", "That is not a content ID"} {
		if !strings.Contains(page, want) {
			t.Errorf("launcher page lacks %q", want)
		}
	}
	if strings.Contains(page, `class="banner"`) {
		t.Error("banner shown with nothing to say")
	}
	page = launcherHTML(launcherView{Version: "1.1.0", Banner: "CanalPeer 1.2.0 is available", BannerAction: "download"})
	for _, want := range []string{`class="banner"`, "CanalPeer 1.2.0 is available", "cpDownload()"} {
		if !strings.Contains(page, want) {
			t.Errorf("banner page lacks %q", want)
		}
	}
}

// Text from the update check is not markup.
func TestLauncherEscapesBanner(t *testing.T) {
	page := launcherHTML(launcherView{Version: "1.1.0", Banner: "<script>x</script>"})
	if strings.Contains(page, "<script>x") {
		t.Fatal("banner text was not escaped")
	}
}

// After updating itself, the launcher lists what changed.
func TestLauncherShowsNotes(t *testing.T) {
	page := launcherHTML(launcherView{Version: "1.5.0", Banner: "Updated to CanalPeer 1.5.0",
		Notes: []string{"Picture in picture", "Works on <Android 7>"}})
	if !strings.Contains(page, `class="notes"`) || !strings.Contains(page, "<li>Picture in picture</li>") ||
		!strings.Contains(page, "&lt;Android 7&gt;") {
		t.Fatalf("notes missing or unescaped:\n%s", page[strings.Index(page, "<body>"):])
	}
	if strings.Contains(launcherHTML(launcherView{Version: "1.5.0"}), `class="notes"`) {
		t.Error("notes shown with nothing to say")
	}
}

func TestLauncherRecent(t *testing.T) {
	page := launcherHTML(launcherView{Version: "1.1.0", Recent: []recentRow{
		{ID: "f16d68fa5ff64f3894a45062ca28e4e66f142c6a", Name: "Canal Uno", Short: "f16d…2c6a", Ago: "2 h ago"},
		{ID: "eab7aeef0218ce8b0752e596e4792b69eda4df5e", Name: "Canal Dos", Short: "eab7…df5e", Ago: "yesterday"},
	}})
	for _, want := range []string{">Recent<", "Canal Uno", "Canal Dos", "2 h ago", "f16d…2c6a",
		`data-id="f16d68fa5ff64f3894a45062ca28e4e66f142c6a"`, "cpForget(", "cpWatch(", "acestream://"} {
		if !strings.Contains(page, want) {
			t.Errorf("launcher page lacks %q", want)
		}
	}
}

func TestLauncherNoRecent(t *testing.T) {
	if page := launcherHTML(launcherView{Version: "1.1.0"}); strings.Contains(page, ">Recent<") {
		t.Error("Recent heading shown with no history")
	}
}

// Channel names come from whoever made the channel: text, not markup.
func TestLauncherEscapesRecentName(t *testing.T) {
	page := launcherHTML(launcherView{Version: "1.1.0", Recent: []recentRow{{ID: "a", Name: "<b>x</b>", Short: "a", Ago: "just now"}}})
	if strings.Contains(page, "<b>x</b>") {
		t.Error("channel name rendered as markup")
	}
}

func TestLauncherSettingsPanel(t *testing.T) {
	page := launcherHTML(launcherView{Version: "1.1.0", Vpn: "on"})
	for _, want := range []string{"⚙", `id="settings"`, "VPN (WARP)", ">Automatic<", ">On<", ">Off<",
		`class="opt on" data-mode="on"`, "cpSetVpn(", "All the app goes through WARP."} {
		if !strings.Contains(page, want) {
			t.Errorf("launcher page lacks %q", want)
		}
	}
	// Closable without Esc: a ✕ in the panel and a click outside it; the ⚙ stays above the panel.
	for _, want := range []string{`id="settingsclose"`, `getElementById("settingsclose").addEventListener("click", () => showPanel(false))`,
		"!panel.contains(e.target)", "z-index: 2"} {
		if !strings.Contains(page, want) {
			t.Errorf("settings panel lacks the close affordance %q", want)
		}
	}
	if strings.Contains(page, `class="opt on" data-mode="auto"`) || strings.Contains(page, `class="opt on" data-mode="off"`) {
		t.Error("more than one mode marked")
	}
	if strings.Contains(page, "In use now: through WARP") {
		t.Error("in-use line shown while WARP is not in use")
	}
	if page := launcherHTML(launcherView{Version: "1.1.0", Vpn: "auto", VpnInUse: true}); !strings.Contains(page, "In use now: through WARP") ||
		!strings.Contains(page, `class="opt on" data-mode="auto"`) {
		t.Error("in-use line or auto mark missing")
	}
}
