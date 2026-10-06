package protocol

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestRejectUntrustedFrames(t *testing.T) {
	for _, s := range []string{`{"v":2,"type":"READY"}` + "\n", `{"v":1,"type":"READY","jit":"secret"}` + "\n", `{"v":1,"type":"READY","extra":true}` + "\n", `{"v":1,"type":"CONFIG","jit":"$oops"}` + "\n", `{"v":1,"type":"DONE","code":-1}` + "\n", `{"v":1,"type":"ACK"} {}` + "\n", strings.Repeat("x", MaxFrame) + "\n", ""} {
		if _, e := NewReader(strings.NewReader(s)).Read(); e == nil {
			t.Fatalf("accepted invalid input of length %d", len(s))
		}
	}
}
func TestRoundTripAndBound(t *testing.T) {
	var b bytes.Buffer
	want := Frame{V: 1, Type: "CONFIG", JIT: base64.StdEncoding.EncodeToString([]byte("test-only"))}
	if e := Write(&b, want); e != nil {
		t.Fatal(e)
	}
	got, e := NewReader(&b).Read()
	if e != nil || got != want {
		t.Fatalf("roundtrip failed: %v", e)
	}
	if Validate(Frame{V: 1, Type: "CONFIG", JIT: strings.Repeat("A", MaxJIT+4)}) == nil {
		t.Fatal("unbounded JIT accepted")
	}
}
func FuzzRead(f *testing.F) {
	f.Add([]byte("{\"v\":1,\"type\":\"READY\"}\n"))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = NewReader(bytes.NewReader(b)).Read() })
}
