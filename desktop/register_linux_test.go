package main

import (
	"strings"
	"testing"
)

func TestDesktopEntryQuotesTheBinaryPath(t *testing.T) {
	e := desktopEntry(`/home/a b/Down"loads/$x/canalpeer`)
	if !strings.Contains(e, "Exec=\"/home/a b/Down\\\"loads/\\$x/canalpeer\" %u\n") {
		t.Fatal(e)
	}
	if !strings.Contains(e, "MimeType=x-scheme-handler/acestream;\n") {
		t.Fatal("does not claim acestream://")
	}
}
