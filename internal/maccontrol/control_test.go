package maccontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func device(payload []byte, nonce string) *bytes.Reader {
	b := make([]byte, DiskBytes)
	copy(b[PayloadOffset:], payload)
	h := sha256.Sum256(payload)
	m, _ := json.Marshal(Commit{1, nonce, len(payload), hex.EncodeToString(h[:])})
	copy(b[ConfigOffset:], m)
	return bytes.NewReader(b)
}

func TestConfigIntegrityAndSession(t *testing.T) {
	n := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	p, _ := json.Marshal(Config{1, n, "CONFIG", "YWJj"})
	if _, err := ReadConfig(device(p, n), n); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(device(p, n), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); err == nil {
		t.Fatal("stale session accepted")
	}
	b := make([]byte, DiskBytes)
	r := device(p, n)
	r.Read(b)
	b[PayloadOffset] ^= 1
	if _, err := ReadConfig(bytes.NewReader(b), n); err == nil {
		t.Fatal("partial/tampered payload accepted")
	}
	if _, err := ReadConfig(bytes.NewReader(b[:ConfigOffset+10]), n); err == nil {
		t.Fatal("incomplete commit accepted")
	}
}

func TestStrictInput(t *testing.T) {
	for _, data := range []string{`{"v":1,"v":2}`, `{"v":1,"unexpected":"secret"}`, `{} {}`, `[]`} {
		var h Header
		if Decode([]byte(data), &h) == nil {
			t.Fatalf("unsafe JSON accepted %s", data)
		}
	}
	if _, err := ParseHeader(make([]byte, Page)); err == nil {
		t.Fatal("blank header accepted")
	}
	p, _ := json.Marshal(Config{1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "CONFIG", "bad encoding"})
	if _, err := ReadConfig(device(p, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("invalid credential encoding accepted")
	}
}
