package mobile

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"canalpeer.com/core/ace"
)

// builtInDescriptors is the site's archive as of the build: make snapshot
// writes descriptors.snapshot.json.gz, which git ignores. A build without it
// carries the empty descriptors.json.gz, the only one in the repo.
var builtInDescriptors = func() []byte {
	if b, err := builtInFS.ReadFile("descriptors.snapshot.json.gz"); err == nil {
		return b
	}
	b, _ := builtInFS.ReadFile("descriptors.json.gz")
	return b
}()

//go:embed descriptors*.json.gz
var builtInFS embed.FS

// siteURL resolves content ids for the apps. During matches the operators
// block the AceStream resolver hosts, the official engine's too (measured
// 2026-10-24); trackers and peers stay reachable, and so does this site.
const siteURL = "https://canalpeer.com"

// siteMirrors hold copies of the site's archive where blocking canalpeer.com
// does not reach: GitHub publishes them hourly, sealed with mirrorKey.
var siteMirrors = []string{"https://raw.githubusercontent.com/pineda89/canalpeer/mirror/data.bin"}

// mirrorKey seals the mirrors' copy (AES-256-GCM). It is no secret from anyone
// who takes the app apart; it only keeps channel names out of plain sight on
// a public repo. .github/workflows/mirror.yml seals with the same value.
var mirrorKey, _ = hex.DecodeString("36955e9a54acb12ea0cffc4ee62b7ea4faab580afd226e0c5d9032ed0f4d6246")

// mirrorMagic starts a sealed copy: magic, 12-byte nonce, GCM ciphertext.
const mirrorMagic = "CPM1"

// openMirror unseals a mirror's copy into the gzipped batch.
func openMirror(key, sealed []byte) ([]byte, error) {
	if len(sealed) < len(mirrorMagic)+12 || string(sealed[:len(mirrorMagic)]) != mirrorMagic {
		return nil, errors.New("mirror: not a sealed copy")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	rest := sealed[len(mirrorMagic):]
	return gcm.Open(nil, rest[:12], rest[12:], nil)
}

// resolveChain finds a descriptor among the channels this device resolved,
// then in its copy of the site's archive, then in the one built into the app,
// then on the site, then on the AceStream hosts. A content id's descriptor
// never changes, so whatever is found is kept for good.
type resolveChain struct {
	cacheDir  string // "" keeps nothing
	builtIn   []byte // gzipped batch shipped inside the app
	site      string
	mirrors   []string      // copies of the site's archive (siteMirrors), read when the site is not
	mirrorKey []byte        // unseals them
	hedge     time.Duration // how long the AceStream hosts wait for the others; 0 = 2 s
	client    *http.Client
	direct    func(context.Context, string) (ace.Descriptor, error)
}

func (rc resolveChain) resolve(ctx context.Context, id string) (ace.Descriptor, error) {
	// No resolver takes an infohash: only the copies of the site's archive
	// know it. Not cached on its own either, so sync never reports it.
	if strings.HasPrefix(id, infohashPrefix) {
		if d, ok := rc.fromCopies(id); ok {
			return d, nil
		}
		for _, m := range rc.mirrors {
			if d, err := rc.fromMirror(ctx, m, id); err == nil {
				return d, nil
			}
		}
		return ace.Descriptor{}, fmt.Errorf("%w: %s is not in the channel archive", ace.ErrNotFound, id)
	}
	// Movies live apart: sync reports every live channel the site lacks, and the
	// site has no use for movies.
	path, moviePath := "", ""
	if rc.cacheDir != "" {
		path = filepath.Join(rc.cacheDir, "descriptors", id+".json")
		moviePath = filepath.Join(rc.cacheDir, "descriptors", "movies", id+".json")
		for _, p := range []string{path, moviePath} {
			if b, err := os.ReadFile(p); err == nil {
				var d ace.Descriptor
				if json.Unmarshal(b, &d) == nil && usable(d) {
					return d, nil
				}
			}
		}
	}
	if d, ok := rc.fromCopies(id); ok {
		return d, nil
	}
	d, err := rc.race(ctx, id)
	if err == nil && path != "" {
		if d.Torrent != nil {
			path = moviePath
		}
		keep(path, d)
	}
	return d, err
}

// viaHosts asks the AceStream hosts alone, through d: the retry through WARP
// when the whole chain failed on the network. What it finds is kept like the
// rest; sync reports it to the site on a later start.
func (rc resolveChain) viaHosts(ctx context.Context, d ace.Dialer, id string) (ace.Descriptor, error) {
	desc, err := ace.NewResolver(d).Resolve(ctx, id)
	if err == nil && rc.cacheDir != "" {
		dir := filepath.Join(rc.cacheDir, "descriptors")
		if desc.Torrent != nil {
			dir = filepath.Join(dir, "movies")
		}
		keep(filepath.Join(dir, id+".json"), desc)
	}
	return desc, err
}

// race asks the site and every mirror at once and takes the first answer: a
// blocked one must cost nothing. The AceStream hosts join after the hedge, or
// as soon as the others have all failed; they are blocked during matches and
// are not ours to hammer.
func (rc resolveChain) race(ctx context.Context, id string) (ace.Descriptor, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		d      ace.Descriptor
		err    error
		direct bool
	}
	results := make(chan result, len(rc.mirrors)+2)
	pending := 0
	launch := func(direct bool, f func() (ace.Descriptor, error)) {
		pending++
		go func() {
			d, err := f()
			results <- result{d, err, direct}
		}()
	}
	var siteErr error
	launch(false, func() (ace.Descriptor, error) {
		d, err := rc.fromSite(ctx, id)
		siteErr = err // read only after this result arrives
		return d, err
	})
	for _, m := range rc.mirrors {
		launch(false, func() (ace.Descriptor, error) { return rc.fromMirror(ctx, m, id) })
	}
	hedge := rc.hedge
	if hedge == 0 {
		hedge = 2 * time.Second
	}
	timer := time.NewTimer(hedge)
	defer timer.Stop()
	asked := false
	askDirect := func() {
		if !asked {
			asked = true
			launch(true, func() (ace.Descriptor, error) { return rc.direct(ctx, id) })
		}
	}
	var lastErr error
	for pending > 0 || !asked {
		if pending == 0 {
			askDirect()
		}
		select {
		case <-ctx.Done():
			return ace.Descriptor{}, ctx.Err()
		case <-timer.C:
			askDirect()
		case r := <-results:
			pending--
			if r.err == nil {
				if r.direct && r.d.Torrent == nil {
					go rc.report(context.WithoutCancel(ctx), id)
				}
				return r.d, nil
			}
			lastErr = r.err
		}
	}
	// The site knows every live channel the service has seen: a blocked host
	// must not turn its "no such channel" into "no network".
	if errors.Is(siteErr, ace.ErrNotFound) {
		return ace.Descriptor{}, siteErr
	}
	return ace.Descriptor{}, lastErr
}

// fromMirror looks a channel up in a mirror's copy of the site's archive.
func (rc resolveChain) fromMirror(ctx context.Context, url, id string) (ace.Descriptor, error) {
	gz, err := rc.fetchMirror(ctx, url)
	if err != nil {
		return ace.Descriptor{}, err
	}
	if d, ok := findIn(gz, id); ok {
		return d, nil
	}
	return ace.Descriptor{}, fmt.Errorf("mirror: %s not listed", id)
}

// fetchMirror downloads a mirror's archive: the gzipped batch, served as is.
func (rc resolveChain) fetchMirror(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := rc.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mirror: HTTP %d", resp.StatusCode)
	}
	sealed, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	body, err := openMirror(rc.mirrorKey, sealed)
	if err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	if _, err := readBatch(body); err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	return body, nil
}

// lastSeq is the newest entry of an archive, 0 when it does not parse.
func lastSeq(gz []byte) int64 {
	b, _ := readBatch(gz)
	var n int64
	for _, e := range b.Items {
		n = max(n, e.Seq)
	}
	return n
}

func (rc resolveChain) fromSite(ctx context.Context, id string) (ace.Descriptor, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rc.site+"/api/descriptor/"+id, nil)
	if err != nil {
		return ace.Descriptor{}, err
	}
	resp, err := rc.client.Do(req)
	if err != nil {
		return ace.Descriptor{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ace.Descriptor{}, ace.ErrNotFound
	default:
		return ace.Descriptor{}, fmt.Errorf("site: HTTP %d", resp.StatusCode)
	}
	var d ace.Descriptor
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return ace.Descriptor{}, fmt.Errorf("site: %w", err)
	}
	if !usable(d) {
		return ace.Descriptor{}, errors.New("site: unusable descriptor")
	}
	return d, nil
}

// usable reports whether a descriptor read from JSON can be played: a movie's
// torrent, or a live channel that passes ace's own check. Whatever fails it
// is treated as not found there.
func usable(d ace.Descriptor) bool {
	return len(d.Torrent) > 0 || d.Check() == nil
}

// keep writes the descriptor where resolve looks first; failing only costs a
// lookup next time.
func keep(path string, d ace.Descriptor) {
	b, _ := json.Marshal(d)
	writeFile(path, b)
}

// batch is the site's descriptor archive, also the format of the copy built
// into the app and of the device's copy of the site's (all.json.gz).
type batch struct {
	Items []entry `json:"items"`
	Next  int64   `json:"next"`
}

type entry struct {
	Seq  int64          `json:"seq"`
	CID  string         `json:"cid"`
	Desc ace.Descriptor `json:"d"`
}

func readBatch(gz []byte) (batch, error) {
	var b batch
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return b, err
	}
	err = json.NewDecoder(zr).Decode(&b)
	return b, err
}

// ponytail: decodes the whole copy per miss (~1 ms for a thousand channels);
// index it once if it grows past tens of thousands.
func findIn(gz []byte, id string) (ace.Descriptor, bool) {
	ih, byInfohash := strings.CutPrefix(id, infohashPrefix)
	b, _ := readBatch(gz)
	for _, e := range b.Items {
		match := e.CID == id
		if byInfohash {
			match = hex.EncodeToString(e.Desc.InfoHash[:]) == ih
		}
		if match && usable(e.Desc) {
			return e.Desc, true
		}
	}
	return ace.Descriptor{}, false
}

func (rc resolveChain) fromCopies(id string) (ace.Descriptor, bool) {
	if rc.cacheDir != "" {
		if gz, err := os.ReadFile(filepath.Join(rc.cacheDir, "descriptors", "all.json.gz")); err == nil {
			if d, ok := findIn(gz, id); ok {
				return d, true
			}
		}
	}
	return findIn(rc.builtIn, id)
}

// report asks the site to resolve a channel this device found without it, so
// the site's archive (and every app built later) gets it too. It fails while
// the site is blocked; sync tries again on the next start.
func (rc resolveChain) report(ctx context.Context, id string) error {
	_, err := rc.fromSite(ctx, id)
	return err
}

var syncing sync.Mutex

// sync replaces the device's copy of the site's archive when it changed
// (ETag), then reports every channel this device keeps that the site lacks.
// Any step may fail while the site is blocked; the next start tries again.
func (rc resolveChain) sync(ctx context.Context) error {
	if rc.cacheDir == "" || !syncing.TryLock() {
		return nil
	}
	defer syncing.Unlock()
	dir := filepath.Join(rc.cacheDir, "descriptors")
	allPath, tagPath := filepath.Join(dir, "all.json.gz"), filepath.Join(dir, "all.etag")
	all, _ := os.ReadFile(allPath)
	all, err := rc.syncSite(ctx, allPath, tagPath, all)
	if err != nil {
		// The site is down or blocked: a mirror may still bring new channels.
		// Reporting needs the site, so that waits for the next start.
		rc.syncMirrors(ctx, allPath)
		return err
	}

	b, _ := readBatch(all)
	known := make(map[string]bool, len(b.Items))
	for _, e := range b.Items {
		known[e.CID] = true
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		if known[id] {
			continue
		}
		if err := rc.report(ctx, id); err != nil && !errors.Is(err, ace.ErrNotFound) {
			return fmt.Errorf("report %s: %w", id, err)
		}
	}
	return nil
}

// syncSite refreshes the device's copy from the site, by ETag, and returns it.
func (rc resolveChain) syncSite(ctx context.Context, allPath, tagPath string, all []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rc.site+"/api/descriptors", nil)
	if err != nil {
		return all, err
	}
	// Set by hand, so the transport leaves the body gzipped: it is kept as is.
	req.Header.Set("Accept-Encoding", "gzip")
	if tag, err := os.ReadFile(tagPath); err == nil && len(all) > 0 {
		req.Header.Set("If-None-Match", string(tag))
	}
	resp, err := rc.client.Do(req)
	if err != nil {
		return all, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			return all, fmt.Errorf("sync: %w", err)
		}
		if resp.Header.Get("Content-Encoding") != "gzip" {
			return all, errors.New("sync: archive not gzipped")
		}
		if _, err := readBatch(body); err != nil {
			return all, fmt.Errorf("sync: %w", err)
		}
		if err := writeFile(allPath, body); err != nil {
			return all, err
		}
		all = body
		writeFile(tagPath, []byte(resp.Header.Get("ETag")))
	default:
		return all, fmt.Errorf("sync: HTTP %d", resp.StatusCode)
	}
	return all, nil
}

// syncMirrors takes the newest mirror copy that is ahead of the device's: a
// mirror that lags must never roll it back.
func (rc resolveChain) syncMirrors(ctx context.Context, allPath string) {
	have, _ := os.ReadFile(allPath)
	best, bestSeq := []byte(nil), lastSeq(have)
	for _, m := range rc.mirrors {
		gz, err := rc.fetchMirror(ctx, m)
		if err != nil {
			log.Print("descriptor mirror: ", err)
			continue
		}
		if n := lastSeq(gz); n > bestSeq {
			best, bestSeq = gz, n
		}
	}
	if best != nil {
		writeFile(allPath, best)
	}
}

func writeFile(path string, b []byte) error {
	os.MkdirAll(filepath.Dir(path), 0o700)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
