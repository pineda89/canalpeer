package cast

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"canalpeer.com/core/route"
)

const lgSSDP = "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nEXT:\r\nLocation: http://192.168.1.42:1752/\r\nST: urn:schemas-upnp-org:device:MediaRenderer:1\r\n\r\n"

func TestParseSSDP(t *testing.T) {
	if l, ok := ParseSSDP([]byte(lgSSDP)); !ok || l != "http://192.168.1.42:1752/" {
		t.Fatalf("got %q %v", l, ok)
	}
	if _, ok := ParseSSDP([]byte("HTTP/1.1 200 OK\r\nST: x\r\n\r\n")); ok {
		t.Fatal("no LOCATION must not be ok")
	}
}

func desc(urlBase, ctl string) string {
	return `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0">` + urlBase + `<device><friendlyName>[LG] webOS TV UK6470PLC</friendlyName><serviceList><service><serviceType>urn:schemas-upnp-org:service:RenderingControl:1</serviceType><controlURL>/rc</controlURL></service><service><serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType><controlURL>` + ctl + `</controlURL></service></serviceList></device></root>`
}

func TestParseDescription(t *testing.T) {
	r, err := ParseDescription("http://192.168.1.42:1752/", []byte(desc("", "/AVTransport/x/control.xml")))
	want := Renderer{"[LG] webOS TV UK6470PLC", "192.168.1.42", "http://192.168.1.42:1752/AVTransport/x/control.xml"}
	if err != nil || r != want {
		t.Fatalf("got %+v %v", r, err)
	}
	r, err = ParseDescription("http://192.168.1.42:1752/d.xml", []byte(desc("<URLBase>http://192.168.1.42:9000/</URLBase>", "avt/c")))
	if err != nil || r.ControlURL != "http://192.168.1.42:9000/avt/c" {
		t.Fatalf("URLBase: %+v %v", r, err)
	}
	if _, err := ParseDescription("http://h/", []byte(`<root><device><friendlyName>x</friendlyName></device></root>`)); err == nil {
		t.Fatal("no AVTransport must fail")
	}
}

// A device's description must not send the control POSTs to another host.
func TestControlURLStaysOnTheDevice(t *testing.T) {
	for _, d := range []string{
		desc("<URLBase>http://10.0.0.9/</URLBase>", "avt/c"),
		desc("", "http://127.0.0.1:8080/avt/c"),
	} {
		if r, err := ParseDescription("http://192.168.1.42:1752/d.xml", []byte(d)); err == nil {
			t.Fatalf("accepted %s", r.ControlURL)
		}
	}
}

func TestLocationMustBeTheSender(t *testing.T) {
	tv := net.ParseIP("192.168.1.42")
	if !locatedAt("http://192.168.1.42:1752/d.xml", tv) {
		t.Fatal("the sender's own location refused")
	}
	for _, loc := range []string{"http://127.0.0.1:80/", "http://example.com/d.xml", "https://192.168.1.42/", "file:///etc/hosts"} {
		if locatedAt(loc, tv) {
			t.Fatalf("accepted %s from %s", loc, tv)
		}
	}
}

func fake(t *testing.T, status int, reply string, got *[2]string) string {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got[0], got[1] = r.Header.Get("SOAPACTION"), string(b)
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

func TestSetURI(t *testing.T) {
	var got [2]string
	u := fake(t, 200, "", &got)
	if err := SetURI(context.Background(), u, "http://10.0.0.2:5/live.ts?a=1&b=2", "Tom & Jerry"); err != nil {
		t.Fatal(err)
	}
	if got[0] != `"urn:schemas-upnp-org:service:AVTransport:1#SetAVTransportURI"` {
		t.Fatalf("SOAPACTION %q", got[0])
	}
	for _, s := range []string{
		"<CurrentURI>http://10.0.0.2:5/live.ts?a=1&amp;b=2</CurrentURI>",
		"&lt;dc:title&gt;Tom &amp;amp; Jerry&lt;/dc:title&gt;",
		"video/mp2t",
	} {
		if !strings.Contains(got[1], s) {
			t.Errorf("body lacks %q:\n%s", s, got[1])
		}
	}
}

func TestPlayStopState(t *testing.T) {
	var got [2]string
	u := fake(t, 200, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:GetTransportInfoResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><CurrentTransportState>PLAYING</CurrentTransportState></u:GetTransportInfoResponse></s:Body></s:Envelope>`, &got)
	ctx := context.Background()
	if err := Play(ctx, u); err != nil || !strings.Contains(got[1], "<Speed>1</Speed>") {
		t.Fatalf("play: %v %s", err, got[1])
	}
	if err := Stop(ctx, u); err != nil || !strings.HasSuffix(got[0], `#Stop"`) {
		t.Fatalf("stop: %v %s", err, got[0])
	}
	if s, err := State(ctx, u); err != nil || s != "PLAYING" {
		t.Fatalf("state %q %v", s, err)
	}
}

func TestSOAPFault(t *testing.T) {
	var got [2]string
	u := fake(t, 500, "<s:Fault/>", &got)
	if err := Play(context.Background(), u); err == nil {
		t.Fatal("500 must be an error")
	}
	if _, err := State(context.Background(), u); err == nil {
		t.Fatal("500 must be an error")
	}
}

func TestCastUsesDirectDialer(t *testing.T) {
	route.Configure("on", t.TempDir())
	defer route.Configure("off", t.TempDir())
	tr, ok := httpClient.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatalf("transport %T", httpClient.Transport)
	}
	if reflect.ValueOf(tr.DialContext).Pointer() != reflect.ValueOf(dialer.DialContext).Pointer() {
		t.Fatal("transport must use the package's plain dialer")
	}
	// Still reaches a LAN fake with WARP forced on.
	var got [2]string
	u := fake(t, 200, "", &got)
	if _, err := url.Parse(u); err != nil {
		t.Fatal(err)
	}
	if err := Play(context.Background(), u); err != nil {
		t.Fatal(err)
	}
}
