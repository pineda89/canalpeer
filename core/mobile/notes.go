package mobile

import (
	"strings"

	"canalpeer.com/core/changelog"
)

// Notes is what changed in version, one item per line, in Spanish for "es" and
// English otherwise, without the computer's items; empty for a version with no
// entry. The app shows it after an update.
func Notes(version, lang string) string {
	return strings.Join(changelog.Notes(version, lang, "android"), "\n")
}
