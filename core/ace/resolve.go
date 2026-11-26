package ace

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Content id -> descriptor resolution without the engine, reverse-engineered
// from engine 3.1.80. All three
// secrets below are global constants of that engine version, not per stream.

// transportKey is md5 of a key embedded in CoreApp.so; IV = sha1(key)[:16].
var (
	transportKey = mustHex("a50c4e33a2f48cc50ce275c9ff3a31bf")
	transportIV  = func() []byte { s := sha1.Sum(transportKey); return s[:16] }()
	signSecret   = mustHex(
		"48212b3a48314e6e7676585c7830625327283b302f415c6e527b247b5c6e2f33" +
			"25315c7830622a5b72306f3e517a4e474b6b585440765c78306233444e3b6778" +
			"5f36364c32207b6046302c5c744b6d3e586f477e6959285c783062755d36457d" +
			"5c747e303726483b39714531643f642d41375328")
)

// infohashFields go into the infohash in exactly this order. The rest of the
// descriptor (trackers, categories, quality...) is mutable metadata.
var infohashFields = []string{
	"name", "authmethod", "pubkey", "piece_length",
	"chunk_length", "bitrate", "provider_key", "provider_sid",
}

// DefaultResolverHosts answer /gettorrent (the s1..s4.torrentstream servers).
var DefaultResolverHosts = []string{
	"5.252.161.191:8081",
	"64.227.119.64:8081",
	"51.158.152.5:8081",
}

const transportMagic = "AceStreamTransport"

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// Descriptor is what a client needs to join a channel's swarm.
type Descriptor struct {
	Name        string
	InfoHash    [20]byte
	Trackers    []string
	PieceLength int64
	ChunkLength int64
	Bitrate     int64 // bytes per second
	// PubKey is the source's key (DER), which signs every piece. A descriptor
	// without it plays unverified.
	PubKey []byte `json:",omitempty"`
	// Torrent is set for a movie: the resolver answered with a plain .torrent
	// instead of a live channel's transport blob. Only Name and Trackers are
	// filled alongside it.
	Torrent []byte `json:",omitempty"`
}

// ErrNotFound means the resolver does not know the content id.
var ErrNotFound = errors.New("content not found")

// NormalizeContentID accepts a 40-hex content id, optionally prefixed with
// acestream://, and returns it lowercased.
func NormalizeContentID(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "acestream://")))
	if len(s) != 40 {
		return "", fmt.Errorf("content id must be 40 hex characters, got %d", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", errors.New("content id must be hex")
	}
	return s, nil
}

// SignQuery computes the _s parameter: sha1 of the sorted "k=v" pairs joined by
// "#", followed by the engine's secret.
func SignQuery(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + params[k]
	}
	sum := sha1.Sum(append([]byte(strings.Join(parts, "#")), signSecret...))
	return hex.EncodeToString(sum[:])
}

// DecryptTransport opens an AceStreamTransport v2 blob: AES-128-CBC, PKCS7.
func DecryptTransport(blob []byte) ([]byte, error) {
	if len(blob) < len(transportMagic)+2 || string(blob[:len(transportMagic)]) != transportMagic {
		return nil, errors.New("not an AceStreamTransport blob")
	}
	body := blob[len(transportMagic)+2:]
	if len(body) == 0 || len(body)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext is not block aligned (%d bytes)", len(body))
	}
	block, err := aes.NewCipher(transportKey)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, transportIV).CryptBlocks(plain, body)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize {
		return nil, errors.New("bad PKCS7 padding")
	}
	for _, b := range plain[len(plain)-pad:] {
		if int(b) != pad {
			return nil, errors.New("bad PKCS7 padding")
		}
	}
	return plain[:len(plain)-pad], nil
}

// ParseDescriptor decodes the plaintext descriptor and derives its infohash:
// sha1 of a bencoded list of [key, value] pairs over infohashFields.
func ParseDescriptor(plain []byte) (Descriptor, error) {
	v, err := Decode(plain)
	if err != nil {
		return Descriptor{}, fmt.Errorf("descriptor: %w", err)
	}
	d, ok := v.(map[string]any)
	if !ok {
		return Descriptor{}, errors.New("descriptor is not a dict")
	}
	var pairs []any
	for _, k := range infohashFields {
		if val, ok := d[k]; ok {
			pairs = append(pairs, []any{k, val})
		}
	}
	enc, err := Encode(pairs)
	if err != nil {
		return Descriptor{}, err
	}
	desc := Descriptor{InfoHash: sha1.Sum(enc)}
	if name, ok := d["name"].([]byte); ok {
		desc.Name = string(name)
	}
	desc.PubKey, _ = d["pubkey"].([]byte)
	desc.PieceLength, _ = dictInt(d, "piece_length")
	desc.ChunkLength, _ = dictInt(d, "chunk_length")
	desc.Bitrate, _ = dictInt(d, "bitrate")
	if list, ok := d["trackers"].([]any); ok {
		for _, t := range list {
			if s, ok := t.([]byte); ok {
				desc.Trackers = append(desc.Trackers, string(s))
			}
		}
	}
	if err := desc.Check(); err != nil {
		return Descriptor{}, err
	}
	return desc, nil
}

// Check reports whether a live channel's descriptor can be played. Every
// source of descriptors must pass it: the coordinator divides by ChunkLength,
// and a chunk's index is 16 bits on the wire.
func (d Descriptor) Check() error {
	if d.PieceLength <= 0 || d.ChunkLength <= 0 || d.PieceLength%d.ChunkLength != 0 ||
		d.PieceLength > 16<<20 || d.ChunkLength > MaxMessageLen/2 || d.PieceLength/d.ChunkLength > 1<<16 {
		return fmt.Errorf("unusable descriptor: piece %d chunk %d", d.PieceLength, d.ChunkLength)
	}
	if len(d.Trackers) == 0 {
		return errors.New("descriptor lists no trackers")
	}
	return nil
}

// Resolver fetches transport blobs from the /gettorrent servers.
type Resolver struct {
	Hosts  []string
	Client *http.Client
}

// NewResolver builds a resolver whose HTTP goes through d.
func NewResolver(d Dialer) *Resolver {
	return &Resolver{
		Hosts: DefaultResolverHosts,
		Client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{DialContext: d.DialContext, Proxy: nil},
		},
	}
}

// Resolve turns a content id into its descriptor, trying each host in turn.
func (r *Resolver) Resolve(ctx context.Context, contentID string) (Descriptor, error) {
	cid, err := NormalizeContentID(contentID)
	if err != nil {
		return Descriptor{}, err
	}
	params := map[string]string{
		"_aa": "arm64-v8a", "_ap": "d3efefe5-4ce4-345b-adb6-adfa3ba92eab",
		"_av": "302131302", "_n": "3.1.80", "_p": "android",
		"_r": strconv.FormatUint(uint64(randUint32())<<32|uint64(randUint32()), 10),
		"_v": "3018000", "pid": cid,
	}
	params["_s"] = SignQuery(params)
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	var errs []error
	for _, host := range r.Hosts {
		desc, err := r.fetch(ctx, "http://"+host+"/gettorrent?"+q.Encode())
		if err == nil || errors.Is(err, ErrNotFound) || ctx.Err() != nil {
			return desc, err
		}
		errs = append(errs, fmt.Errorf("%s: %w", host, err))
	}
	return Descriptor{}, errors.Join(errs...)
}

func (r *Resolver) fetch(ctx context.Context, u string) (Descriptor, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Descriptor{}, err
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return Descriptor{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // a movie's .torrent can pass 1 MiB in base64
	if err != nil {
		return Descriptor{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Descriptor{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// Measured 2026-10-22: an unknown content id gets HTTP 200 with an empty body.
	if len(strings.TrimSpace(string(body))) == 0 {
		return Descriptor{}, ErrNotFound
	}
	var parsed struct {
		Torrent string `xml:"torrent"`
		Error   string `xml:"error"`
	}
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return Descriptor{}, fmt.Errorf("xml: %w", err)
	}
	if parsed.Torrent == "" {
		// The resolver answers unknown ids with a response lacking <torrent>.
		return Descriptor{}, fmt.Errorf("%w: %s", ErrNotFound, strings.TrimSpace(parsed.Error))
	}
	blob, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(parsed.Torrent), ""))
	if err != nil {
		return Descriptor{}, fmt.Errorf("base64: %w", err)
	}
	if !strings.HasPrefix(string(blob), transportMagic) {
		return parseTorrent(blob)
	}
	plain, err := DecryptTransport(blob)
	if err != nil {
		return Descriptor{}, err
	}
	return ParseDescriptor(plain)
}

// parseTorrent accepts a movie's plain .torrent (measured 2026-10-29: the
// resolver sends these unencrypted).
func parseTorrent(raw []byte) (Descriptor, error) {
	v, err := Decode(raw)
	if err != nil {
		return Descriptor{}, fmt.Errorf("torrent: %w", err)
	}
	d, _ := v.(map[string]any)
	info, ok := d["info"].(map[string]any)
	if !ok {
		return Descriptor{}, errors.New("neither a transport blob nor a torrent")
	}
	desc := Descriptor{Torrent: raw}
	if name, ok := info["name"].([]byte); ok {
		desc.Name = string(name)
	}
	add := func(t any) {
		if s, ok := t.([]byte); ok && !slices.Contains(desc.Trackers, string(s)) {
			desc.Trackers = append(desc.Trackers, string(s))
		}
	}
	add(d["announce"])
	if tiers, ok := d["announce-list"].([]any); ok {
		for _, tier := range tiers {
			if list, ok := tier.([]any); ok {
				for _, t := range list {
					add(t)
				}
			}
		}
	}
	return desc, nil
}
