package main

import (
	"canalpeer.com/core/mobile"
	"reflect"
	"strings"
	"testing"
)

// fakeFlow scripts what the launcher and the player do, and records the calls.
type fakeFlow struct {
	launches []func(links <-chan string) (string, bool)
	endings  []ending
	calls    []string
}

func (f *fakeFlow) launch(links <-chan string) (string, bool) {
	f.calls = append(f.calls, "launch")
	next := f.launches[0]
	f.launches = f.launches[1:]
	return next(links)
}

func (f *fakeFlow) play(link string, _ <-chan string) ending {
	f.calls = append(f.calls, "play "+link)
	e := f.endings[0]
	f.endings = f.endings[1:]
	return e
}

func submits(link string) func(<-chan string) (string, bool) {
	return func(<-chan string) (string, bool) { return link, false }
}

func closes(<-chan string) (string, bool) { return "", true }

func TestFlowFromLinkBackToLauncher(t *testing.T) {
	f := &fakeFlow{launches: []func(<-chan string) (string, bool){closes}, endings: []ending{endBack}}
	runFlow("acestream://a", nil, f.launch, f.play)
	if want := []string{"play acestream://a", "launch"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
}

func TestFlowQuitFromPlayerEndsApp(t *testing.T) {
	f := &fakeFlow{launches: []func(<-chan string) (string, bool){submits("acestream://b")}, endings: []ending{endQuit}}
	runFlow("", nil, f.launch, f.play)
	if want := []string{"launch", "play acestream://b"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
}

func TestFlowLinkWhileLauncherPlaysIt(t *testing.T) {
	links := make(chan string, 1)
	links <- "acestream://c"
	waits := func(l <-chan string) (string, bool) { return <-l, false }
	f := &fakeFlow{launches: []func(<-chan string) (string, bool){waits}, endings: []ending{endQuit}}
	runFlow("", links, f.launch, f.play)
	if want := []string{"launch", "play acestream://c"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
}

func TestFlowBackTwiceThenClose(t *testing.T) {
	f := &fakeFlow{
		launches: []func(<-chan string) (string, bool){submits("acestream://d"), submits("acestream://e"), closes},
		endings:  []ending{endBack, endBack},
	}
	runFlow("", nil, f.launch, f.play)
	want := []string{"launch", "play acestream://d", "launch", "play acestream://e", "launch"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
}

func TestLauncherRejectsBadLink(t *testing.T) {
	if link, errText := validate("nonsense"); link != "" || errText != "That is not a content ID or acestream:// link." {
		t.Fatalf("validate(nonsense) = %q, %q", link, errText)
	}
	id := strings.Repeat("ab", 20)
	if link, errText := validate("  " + id + " "); link != id || errText != "" {
		t.Fatalf("validate(id) = %q, %q", link, errText)
	}
	// What validate accepts, the player must too: an infohash channel as well.
	for _, in := range []string{id, "acestream://" + id, "ih-" + id, "http://127.0.0.1:6878/ace/getstream?infohash=" + id} {
		link, errText := validate(in)
		if _, err := mobile.Normalize(link); errText != "" || err != nil {
			t.Fatalf("validate(%q) = %q, %q; the player says %v", in, link, errText, err)
		}
	}
}

// No launcher (no web view on this machine): today's idle player window, once.
func TestFlowWithoutLauncher(t *testing.T) {
	f := &fakeFlow{endings: []ending{endBack}}
	runFlow("", nil, nil, f.play)
	if want := []string{"play "}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
}

// The launcher window could not be made (a broken web view): fall back to the
// player window alone, never quit without showing anything.
func TestFlowLauncherFailsFallsBackToPlayer(t *testing.T) {
	f := &fakeFlow{launches: []func(<-chan string) (string, bool){func(<-chan string) (string, bool) { return "", false }}, endings: []ending{endBack}}
	runFlow("", nil, f.launch, f.play)
	if want := []string{"launch", "play "}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
}
