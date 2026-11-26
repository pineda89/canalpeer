// Package cast discovers DLNA/UPnP MediaRenderers on the LAN and drives their
// AVTransport service. It never uses package route: LAN traffic must not go
// through WARP.
package cast

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var dialer = &net.Dialer{Timeout: 3 * time.Second}

// httpClient talks to renderers directly, whatever route is configured to do.
var httpClient = &http.Client{
	Timeout:   4 * time.Second,
	Transport: &http.Transport{DialContext: dialer.DialContext},
	// A renderer has no reason to send us elsewhere; a redirect is an answer.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

const search = "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: urn:schemas-upnp-org:device:MediaRenderer:1\r\n\r\n"

// ParseSSDP extracts the LOCATION header of an SSDP response.
func ParseSSDP(b []byte) (location string, ok bool) {
	for _, line := range strings.Split(string(b), "\n") {
		k, v, found := strings.Cut(strings.TrimSpace(line), ":")
		if found && strings.EqualFold(strings.TrimSpace(k), "location") {
			v = strings.TrimSpace(v)
			return v, v != ""
		}
	}
	return "", false
}

// Search multicasts an M-SEARCH for wait and returns the renderers that
// answered and expose AVTransport, one per IP.
func Search(ctx context.Context, wait time.Duration) ([]Renderer, error) {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	dst := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	for i := 0; i < 2; i++ {
		if _, err := conn.WriteTo([]byte(search), dst); err != nil {
			return nil, err
		}
	}
	// Only listening ends with wait: a TV that answers late still gets its
	// description read (httpClient's timeout bounds that).
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	go func() { <-wctx.Done(); conn.SetReadDeadline(time.Now()) }()

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = map[string]bool{}
		out  = []Renderer{}
	)
	buf := make([]byte, 4096)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		loc, ok := ParseSSDP(buf[:n])
		if !ok || !locatedAt(loc, from.IP) {
			continue
		}
		mu.Lock()
		dup := seen[loc]
		seen[loc] = true
		mu.Unlock()
		if dup {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := describe(ctx, loc)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if !seenIP(out, r.IP) {
				out = append(out, r)
			}
		}()
	}
	wg.Wait()
	return out, nil
}

// locatedAt reports whether loc is an http URL on the host that sent it: any
// machine on the LAN can answer the search, and must not point us at another.
func locatedAt(loc string, from net.IP) bool {
	u, err := url.Parse(loc)
	if err != nil || u.Scheme != "http" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.Equal(from)
}

func seenIP(rs []Renderer, ip string) bool {
	for _, r := range rs {
		if r.IP == ip {
			return true
		}
	}
	return false
}

func describe(ctx context.Context, loc string) (Renderer, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", loc, nil)
	if err != nil {
		return Renderer{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Renderer{}, err
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	if _, err := b.ReadFrom(http.MaxBytesReader(nil, resp.Body, 1<<20)); err != nil {
		return Renderer{}, err
	}
	return ParseDescription(loc, b.Bytes())
}
