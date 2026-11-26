// Package stats tells canalpeer.com that the app was opened and that a
// channel started playing. Every send is best effort: it runs on its own
// goroutine, any HTTP answer ends it, and a failure is only logged.
package stats

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"canalpeer.com/core/route"
)

// Site receives the events; tests replace it.
var Site = "https://canalpeer.com"

// Roots, when set, is the trust store for Site (package mobile adds Let's
// Encrypt's root, which Android 7.0 lacks); nil is the system's.
var Roots func() *x509.CertPool

// Open reports that the app was opened. Without a dataDir no install id can
// be kept, so nothing is sent.
func Open(platform, version, dataDir string) {
	if dataDir == "" {
		log.Print("stats: no data dir, open not reported")
		return
	}
	go func() {
		id, err := InstallID(dataDir)
		if err != nil {
			log.Print("stats: ", err)
			return
		}
		report("/api/app/open", map[string]string{"install": id, "platform": platform, "version": version})
	}()
}

// Play reports that a channel, by its normalized content id, started playing.
func Play(id string) {
	go report("/api/app/play", map[string]string{"id": id})
}

func report(path string, body any) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := send(ctx, path, body); err != nil {
		log.Print("stats: ", path, ": ", err)
	}
}

var validID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// InstallID is this install's random id, kept in <dataDir>/install-id and
// created on the first call, or again when the file holds anything else.
func InstallID(dataDir string) (string, error) {
	file := filepath.Join(dataDir, "install-id")
	if data, err := os.ReadFile(file); err == nil {
		if id := strings.TrimSpace(string(data)); validID.MatchString(id) {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	b := make([]byte, 16)
	rand.Read(b)
	id := hex.EncodeToString(b)
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(id), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, file); err != nil {
		return "", err
	}
	return id, nil
}

// send POSTs body to Site+path along the current route. When no answer came
// at all and WARP is allowed but not in use, it tries once more through WARP.
func send(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	c := route.HTTPClient(5 * time.Second)
	t := c.Transport.(*http.Transport)
	if Roots != nil {
		t.TLSClientConfig = &tls.Config{RootCAs: Roots()}
	}
	err = post(ctx, c, path, data)
	var answered *httpStatus
	if err == nil || errors.As(err, &answered) || route.Mode() == "off" || route.InUse() {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	w, werr := route.Warp(wctx)
	if werr != nil {
		return errors.Join(err, werr)
	}
	t.DialContext = w.DialContext
	return post(ctx, c, path, data)
}

type httpStatus struct{ code int }

func (e *httpStatus) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

func post(ctx context.Context, c *http.Client, path string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Site+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &httpStatus{resp.StatusCode}
	}
	return nil
}
