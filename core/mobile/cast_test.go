package mobile

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A TV still TRANSITIONING after SetAVTransportURI may refuse the Play that
// follows straight away: DlnaPlay tries Play once more a moment later.
func TestDlnaPlayRetriesPlayOnce(t *testing.T) {
	var actions []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("SOAPACTION")
		actions = append(actions, a[strings.LastIndex(a, "#")+1:len(a)-1])
		if len(actions) == 2 { // the first Play
			w.WriteHeader(500)
		}
	}))
	defer s.Close()
	if err := DlnaPlay(s.URL, "http://192.168.1.2:1/live.ts", "Channel"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(actions, ","); got != "SetAVTransportURI,Play,Play" {
		t.Fatalf("actions %s", got)
	}
}

// Measured on an LG webOS TV: it starts playing on SetAVTransportURI alone,
// leaves the Play that follows hanging, then faults it while TRANSITIONING. A
// refused Play is no failed send: a TV that never reads ends it by endRule.
func TestDlnaPlayIgnoresARefusedPlay(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.Header.Get("SOAPACTION"), `#Play"`) {
			w.WriteHeader(500)
		}
	}))
	defer s.Close()
	if err := DlnaPlay(s.URL, "http://192.168.1.2:1/live.ts", "Channel"); err != nil {
		t.Fatal(err)
	}
}

func TestDlnaPlayFailsWhenTheURIIsRefused(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer s.Close()
	if DlnaPlay(s.URL, "http://192.168.1.2:1/live.ts", "Channel") == nil {
		t.Fatal("want an error")
	}
}

// Measured on an LG webOS TV: SetAVTransportURI fetches the URL there and
// then, and now and then faults with 716 "Resource not found" on a share that
// a second later plays fine. One more try a moment later.
func TestDlnaPlayRetriesARefusedURIOnce(t *testing.T) {
	uris := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.Header.Get("SOAPACTION"), `#SetAVTransportURI"`) {
			if uris++; uris == 1 {
				w.WriteHeader(500)
			}
		}
	}))
	defer s.Close()
	if err := DlnaPlay(s.URL, "http://192.168.1.2:1/live.ts", "Channel"); err != nil {
		t.Fatal(err)
	}
	if uris != 2 {
		t.Fatalf("SetAVTransportURI sent %d times", uris)
	}
}
