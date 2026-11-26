package mobile

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"canalpeer.com/core/cast"
)

// DlnaSearch looks for DLNA TVs for waitMs and returns JSON
// [{"name","ip","control"}]; "[]" on error.
func DlnaSearch(waitMs int) string {
	rs, err := cast.Search(context.Background(), time.Duration(waitMs)*time.Millisecond)
	if err != nil {
		return "[]"
	}
	type item struct {
		Name    string `json:"name"`
		IP      string `json:"ip"`
		Control string `json:"control"`
	}
	out := make([]item, 0, len(rs))
	for _, r := range rs {
		out = append(out, item{r.Name, r.IP, r.ControlURL})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// DlnaPlay points the TV at url and starts it. A TV still TRANSITIONING
// after the new URI may refuse Play: it gets one more a second later. Only a
// refused URI is an error: an LG plays on the URI alone and faults the Play
// (measured), and a TV that never reads the stream is ended by endRule.
func DlnaPlay(control, url, title string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if cast.SetURI(ctx, control, url, title) != nil {
		// An LG fetches the URL on SetAVTransportURI and now and then faults
		// 716 on a share it plays fine a moment later (measured).
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := cast.SetURI(ctx, control, url, title); err != nil {
			return err
		}
	}
	if cast.Play(ctx, control) == nil {
		return nil
	}
	select {
	case <-time.After(time.Second):
	case <-ctx.Done():
		return nil
	}
	if err := cast.Play(ctx, control); err != nil {
		log.Print("dlna play: ", err)
	}
	return nil
}

// DlnaStop stops playback on the TV; errors are ignored.
func DlnaStop(control string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cast.Stop(ctx, control)
}

// DlnaState is the TV's transport state, "" on error.
func DlnaState(control string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := cast.State(ctx, control)
	if err != nil {
		return ""
	}
	return s
}
