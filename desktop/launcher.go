package main

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"html/template"
)

//go:embed launcher.html
var launcherPage string

//go:embed mark.svg
var markSVG string

//go:embed fonts/Outfit-Regular.ttf
var fontRegular []byte

//go:embed fonts/Outfit-SemiBold.ttf
var fontSemiBold []byte

//go:embed fonts/Outfit-Bold.ttf
var fontBold []byte

var launcherTmpl = template.Must(template.New("launcher").Parse(launcherPage))

// launcherView is what the launcher shows besides its fixed content.
type launcherView struct {
	Version      string
	Banner       string      // update line; empty hides it
	BannerAction string      // "download": the viewer fetches the update themselves
	Notes        []string    // what changed, right after updating itself
	Recent       []recentRow // the channels played here, newest first
	Vpn          string      // the WARP mode chosen: "auto", "on" or "off"
	VpnInUse     bool        // connections go through WARP right now
}

// launcherHTML is the whole launcher page: fonts and mark inline, so it shows
// without network and from any web view.
func launcherHTML(v launcherView) string {
	var b bytes.Buffer
	launcherTmpl.Execute(&b, struct {
		launcherView
		Mark                                template.HTML
		FontRegular, FontSemiBold, FontBold string
	}{v, template.HTML(markSVG),
		base64.StdEncoding.EncodeToString(fontRegular),
		base64.StdEncoding.EncodeToString(fontSemiBold),
		base64.StdEncoding.EncodeToString(fontBold)})
	return b.String()
}
