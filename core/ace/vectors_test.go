package ace

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// vectors are golden outputs of the Python reference client
// (p2p_client/gen_vectors.py, removed; see git history). The port must match them byte for byte.
type vectors struct {
	Seed          string `json:"seed"`
	Pubkey        string `json:"pubkey"`
	Handshake     string `json:"handshake"`
	Request       string `json:"request"`
	Interested    string `json:"interested"`
	BencodeNested string `json:"bencode_nested"`
	ExtHandshake  string `json:"ext_handshake_1_2_3_4"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
