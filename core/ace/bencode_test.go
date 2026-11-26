package ace

import (
	"bytes"
	"os"
	"testing"
)

func TestBencodeRoundTrip(t *testing.T) {
	for _, c := range []string{"i42e", "i-7e", "i0e", "4:spam", "0:", "le", "de", "li1ei2ee", "d3:bar4:spam3:fooi42ee"} {
		v, err := Decode([]byte(c))
		if err != nil {
			t.Fatalf("%q: %v", c, err)
		}
		got, err := Encode(v)
		if err != nil {
			t.Fatalf("%q: %v", c, err)
		}
		if string(got) != c {
			t.Errorf("round trip %q -> %q", c, got)
		}
	}
}

func TestBencodeSortsKeysLikePython(t *testing.T) {
	v := loadVectors(t)
	got, err := Encode(map[string]any{"z": []any{1, []byte("x"), map[string]any{"b": -2, "a": []byte{}}}, "a": 0})
	if err != nil {
		t.Fatal(err)
	}
	if want := unhex(t, v.BencodeNested); !bytes.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBencodeDecodeErrors(t *testing.T) {
	for _, c := range []string{"", "i42", "4:spa", "l", "li1e", "d3:foo", "di1ei2ee", "x", "i4x2e", "-1:a", "i1ei2e", "99999999999999999999:"} {
		if _, err := Decode([]byte(c)); err == nil {
			t.Errorf("%q: expected an error", c)
		}
	}
}

func TestBencodeRejectsDeepNesting(t *testing.T) {
	deep := bytes.Repeat([]byte("l"), 10000)
	if _, err := Decode(deep); err == nil {
		t.Fatal("expected an error")
	}
}

func TestBencodeEncodeRejectsUnknownTypes(t *testing.T) {
	if _, err := Encode(3.5); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := Encode(map[string]any{"a": true}); err == nil {
		t.Fatal("expected an error")
	}
}

// ext_template.bin is a real ext-handshake captured from the engine.
func TestBencodeDecodesEngineExtHandshake(t *testing.T) {
	raw, err := os.ReadFile("testdata/ext_template.bin")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	d := v.(map[string]any)
	if p, _ := dictInt(d, "p"); p != 8621 {
		t.Errorf("p = %d", p)
	}
	if ver, _ := dictInt(d, "v"); ver != 3018000 {
		t.Errorf("v = %d", ver)
	}
	mi := d["mi"].(map[string]any)
	if lsp, _ := dictInt(mi, "lsp"); lsp != 65520291 {
		t.Errorf("mi.lsp = %d", lsp)
	}
	if n := len(d["node_id"].([]byte)); n != 32 {
		t.Errorf("node_id len %d", n)
	}
}
