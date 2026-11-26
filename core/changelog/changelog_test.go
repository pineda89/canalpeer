package changelog

import (
	"strings"
	"testing"
)

func TestEveryEntryHasBothLanguages(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range All() {
		if seen[e.Version] {
			t.Errorf("%s twice", e.Version)
		}
		seen[e.Version] = true
		if len(e.En) == 0 || len(e.En) != len(e.Es) {
			t.Errorf("%s: %d English items, %d Spanish", e.Version, len(e.En), len(e.Es))
		}
	}
}

func TestNotesInTheViewersLanguage(t *testing.T) {
	es := Notes("1.4.3", "es", "android")
	en := Notes("1.4.3", "fr", "android") // no French notes: English
	if len(es) != 1 || !strings.Contains(es[0], "sonido") || len(en) != 1 || !strings.Contains(en[0], "sound") {
		t.Fatalf("es %q en %q", es, en)
	}
	if Notes("9.9.9", "en", "") != nil {
		t.Fatal("notes for an unknown version")
	}
}

// GitHub gets only released versions, newest first.
func TestMarkdownSkipsUnreleased(t *testing.T) {
	md := Markdown()
	if strings.Contains(md, "## 1.5.0") && Find("1.5.0").Date == "" {
		t.Fatal("unreleased version in the markdown")
	}
	if !strings.Contains(md, "## 1.4.4 (2026-11-01)\n\n- ") || strings.Index(md, "## 1.4.4") > strings.Index(md, "## 1.4.3") {
		t.Fatalf("markdown:\n%s", md)
	}
	if !strings.Contains(md, "## 1.3.0 · desktop 1.2.0 (2026-10-29)") {
		t.Fatal("older versions name the desktop's number")
	}
}

func TestReadyForRelease(t *testing.T) {
	if err := Ready("1.4.4"); err != nil {
		t.Fatal(err)
	}
	if Ready("9.9.9") == nil {
		t.Fatal("a version with no entry is not ready")
	}
}

// Each app sees its own items: "Android…" only on Android, "Windows…"/"Mac…" only on the computer.
func TestNotesForEachApp(t *testing.T) {
	if n := Notes("1.4.3", "en", "desktop"); len(n) != 0 {
		t.Fatalf("an Android-only change on desktop: %q", n)
	}
	if n := Notes("1.2.0", "en", "android"); len(n) != 2 { // not "Windows: a single file"
		t.Fatalf("android 1.2.0: %q", n)
	}
	if n := Notes("1.4.2", "en", "desktop"); len(n) != 1 {
		t.Fatalf("a change for everyone is missing on desktop: %q", n)
	}
	if n := Notes("1.2.0", "en", ""); len(n) != 3 {
		t.Fatalf("the site and GitHub get every item: %q", n)
	}
}
