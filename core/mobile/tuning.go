package mobile

import "canalpeer.com/core/ace"

// coreBufferSeconds is how far behind live the core plays. ExoPlayer adds its
// own ~10 s on top, for the ~30 s the spec accepts in exchange for no cuts.
const coreBufferSeconds = 20

// tuningFor turns a buffer in seconds into the core's piece counts.
// ponytail: starting point for the calibration task; tune against measured
// cuts on a real Fire TV, not on paper.
func tuningFor(bitrate, pieceLength int64, seconds int) ace.Tuning {
	t := ace.DefaultTuning()
	depth := 48 // bitrate unknown
	if bitrate > 0 && pieceLength > 0 {
		need := int64(seconds) * bitrate
		depth = int((need + pieceLength - 1) / pieceLength)
		depth = min(max(depth, 12), 200)
	}
	t.BufferDepth = depth
	t.MaxLag = 4 * depth
	// Past SkipAfter too: skipping a stuck piece needs that many complete
	// ones beyond it, with room for a second piece that never arrives.
	t.RequestAhead = max(depth+4, t.SkipAfter+t.SkipAfter/2)
	return t
}
