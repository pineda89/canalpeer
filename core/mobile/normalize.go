package mobile

import (
	"errors"
	"net/url"
	"strings"

	"canalpeer.com/core/ace"
)

// infohashPrefix marks a channel known only by its swarm's infohash, as apps
// with their own engine hand it over (?infohash=). It is not a content id: no
// resolver takes it, only the copies of the site's archive.
const infohashPrefix = "ih-"

// Normalize turns whatever the browser, the user or another app hands over
// into a content id, or infohashPrefix and an infohash: any case, surrounding
// spaces, a trailing slash, a query or a fragment, "acestream:" with or without
// slashes, and a local engine's URL (http://127.0.0.1:6878/ace/getstream?id=…),
// which is what apps like Ace Serve hand to a player.
func Normalize(input string) (string, error) {
	s := strings.TrimSpace(input)
	if u, err := url.Parse(s); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		q := u.Query()
		if id := q.Get("id"); id != "" {
			return ace.NormalizeContentID(id)
		}
		if ih := q.Get("infohash"); ih != "" {
			return withInfohash(ih)
		}
		return "", errors.New("not an AceStream engine link")
	}
	s = strings.ToLower(s)
	if ih, ok := strings.CutPrefix(s, infohashPrefix); ok {
		return withInfohash(ih)
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "acestream://"), "acestream:")
	return ace.NormalizeContentID(strings.TrimRight(s, "/"))
}

// withInfohash checks an infohash (40 hex, like a content id) and marks it.
func withInfohash(s string) (string, error) {
	h, err := ace.NormalizeContentID(s)
	if err != nil {
		return "", errors.New("infohash must be 40 hex characters")
	}
	return infohashPrefix + h, nil
}
