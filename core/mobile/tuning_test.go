package mobile

import (
	"strings"
	"testing"

	"canalpeer.com/core/ace"
)

func TestTuningFromBitrate(t *testing.T) {
	cases := []struct {
		bitrate, piece int64
		depth          int
	}{
		{1_000_000, 262_144, 77},  // ceil(20*1e6/262144) = 77
		{500_000, 1_048_576, 12},  // 10, raised to the minimum
		{10_000_000, 65_536, 200}, // capped
		{0, 262_144, 48},          // unknown bitrate
	}
	def := ace.DefaultTuning()
	for _, c := range cases {
		tu := tuningFor(c.bitrate, c.piece, 20)
		if tu.BufferDepth != c.depth || tu.MaxLag != 4*c.depth || tu.RequestAhead != max(c.depth+4, 24) {
			t.Errorf("%d B/s, %d B pieces: got depth %d lag %d ahead %d, want depth %d",
				c.bitrate, c.piece, tu.BufferDepth, tu.MaxLag, tu.RequestAhead, c.depth)
		}
		if tu.SkipAfter != def.SkipAfter || tu.AssignCooldown != def.AssignCooldown {
			t.Errorf("skip/cooldown changed: %+v", tu)
		}
	}
}

func TestNormalizeAcceptsBrowserVariants(t *testing.T) {
	const id = "f16d68fa5ff64f3894a45062ca28e4e66f142c6a"
	for _, in := range []string{
		id,
		"acestream://" + id,
		"acestream://" + id + "/",
		"  ACESTREAM://" + strings.ToUpper(id) + "?x=1 ",
		"acestream://" + id + "#t",
	} {
		got, err := Normalize(in)
		if err != nil || got != id {
			t.Errorf("Normalize(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "acestream://", "acestream://xyz", id[:39]} {
		if _, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q) accepted", in)
		}
	}
}

// Ace Serve (org.free.aceserve 1.5.7) runs its own engine and hands players
// http://127.0.0.1:6878/ace/getstream?id=… or ?infohash=… as video/*.
func TestNormalizeAcceptsEngineURLs(t *testing.T) {
	const id = "f16d68fa5ff64f3894a45062ca28e4e66f142c6a"
	for in, want := range map[string]string{
		"http://127.0.0.1:6878/ace/getstream?id=" + id:                        id,
		"http://localhost:6878/ace/getstream?pid=x&id=" + strings.ToUpper(id): id,
		"http://127.0.0.1:6878/ace/getstream?infohash=" + id:                  "ih-" + id,
		"ih-" + id:        "ih-" + id,
		"acestream:" + id: id,
	} {
		if got, err := Normalize(in); err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"http://example.com/video.mp4",
		"http://127.0.0.1:6878/ace/getstream?infohash=zz",
		"ih-xyz",
	} {
		if got, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q) accepted as %q", in, got)
		}
	}
}
