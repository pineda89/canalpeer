package ace

import (
	"bytes"
	"crypto/ed25519"
	"net"
	"os"
	"testing"
)

// The field set must match the Python reference client; only the signing
// order differs (see SignExtHandshake), so node_id/signature are compared apart.
func TestExtHandshakeFieldsMatchPython(t *testing.T) {
	v := loadVectors(t)
	key := ed25519.NewKeyFromSeed(unhex(t, v.Seed))
	got, err := BuildExtHandshake(8621, net.ParseIP("1.2.3.4"), key)
	if err != nil {
		t.Fatal(err)
	}
	want := unhex(t, v.ExtHandshake)
	if got[0] != 0 || want[0] != 0 {
		t.Fatal("sub-id must be 0")
	}
	gd, err := Decode(got[1:])
	if err != nil {
		t.Fatal(err)
	}
	// Python's payload is not standard-sorted, so decode it leniently.
	wd, _, err := decodeAt(want[1:], 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	g, w := gd.(map[string]any), wd.(map[string]any)
	if !bytes.Equal(g["node_id"].([]byte), unhex(t, v.Pubkey)) {
		t.Fatal("node_id is not our public key")
	}
	delete(g, "signature")
	delete(w, "signature")
	ge, _ := Encode(g)
	we, _ := Encode(w)
	if !bytes.Equal(ge, we) {
		t.Fatalf("fields differ from Python\n got %q\nwant %q", ge, we)
	}
}

func TestExtHandshakeVerifies(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	eh, err := BuildExtHandshake(6881, nil, key)
	if err != nil {
		t.Fatal(err)
	}
	if eh[0] != 0 {
		t.Fatalf("sub-id %d", eh[0])
	}
	if _, err := Decode(eh[1:]); err != nil {
		t.Fatalf("payload must be canonical bencode: %v", err)
	}
	if again, _ := Encode(mustDecode(t, eh[1:])); !bytes.Equal(again, eh[1:]) {
		t.Fatal("payload must travel with standard sorted keys, like the engine's")
	}
	ok, err := VerifyExtHandshake(eh[1:])
	if err != nil || !ok {
		t.Fatalf("own signature does not verify: %v", err)
	}
	d, _ := Decode(eh[1:])
	if _, has := d.(map[string]any)["yourip"]; has {
		t.Fatal("yourip without a peer IP")
	}
}

// The strongest check on the key order: the real engine's signature over its
// own captured ext-handshake verifies against our serialization.
func TestVerifiesEngineSignature(t *testing.T) {
	raw, err := os.ReadFile("testdata/ext_template.bin")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyExtHandshake(raw)
	if err != nil || !ok {
		t.Fatalf("engine signature does not verify over our serialization: %v", err)
	}
	tampered := bytes.Replace(raw, []byte("i8621e"), []byte("i8622e"), 1)
	if ok, _ := VerifyExtHandshake(tampered); ok {
		t.Fatal("tampered message verified")
	}
}

func mustDecode(t *testing.T, b []byte) any {
	t.Helper()
	v, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
