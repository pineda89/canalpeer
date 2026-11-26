//go:build live

package warp

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestLiveWarp(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	a, err := Register(ctx, &http.Client{Timeout: 15 * time.Second}, APIURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("registered in %v, host %s", time.Since(start).Round(time.Millisecond), a.Host)
	for _, ep := range Endpoints(a) {
		start = time.Now()
		tun, err := Up(ctx, a, ep, TraceCheck)
		if err != nil {
			t.Logf("%s: %v", ep, err)
			continue
		}
		t.Logf("%s: warp=on in %v", ep, time.Since(start).Round(time.Millisecond))
		tun.Close()
		return
	}
	t.Fatal("no endpoint passed TraceCheck")
}
