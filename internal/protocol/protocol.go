// Package protocol implements bounded, versioned newline-delimited JSON framing.
package protocol

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

const MaxFrame = 64 * 1024
const MaxJIT = 48 * 1024
const MaxLogs = 8 * 1024 * 1024

type Frame struct {
	V    int    `json:"v"`
	Type string `json:"type"`
	JIT  string `json:"jit,omitempty"`
	Data string `json:"data,omitempty"`
	Code int    `json:"code,omitempty"`
}
type Reader struct{ r *bufio.Reader }

func NewReader(r io.Reader) *Reader { return &Reader{bufio.NewReaderSize(r, MaxFrame)} }
func (r *Reader) Read() (Frame, error) {
	var f Frame
	b, e := r.r.ReadSlice('\n')
	if e != nil || len(b) > MaxFrame {
		return f, errors.New("invalid or incomplete serial frame")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&f) != nil || d.Decode(new(any)) != io.EOF {
		return Frame{}, errors.New("invalid serial JSON")
	}
	if e = Validate(f); e != nil {
		return Frame{}, e
	}
	return f, nil
}
func Validate(f Frame) error {
	if f.V != 1 {
		return errors.New("unsupported protocol version")
	}
	switch f.Type {
	case "HELLO", "READY", "ACK", "RUNNING":
		if f.JIT != "" || f.Data != "" || f.Code != 0 {
			return errors.New("unexpected fields")
		}
	case "CONFIG":
		if len(f.JIT) == 0 || len(f.JIT) > MaxJIT || f.Data != "" || f.Code != 0 {
			return errors.New("invalid configuration")
		}
		if _, e := base64.StdEncoding.DecodeString(f.JIT); e != nil {
			return errors.New("invalid JIT encoding")
		}
	case "LOG":
		if f.JIT != "" || f.Code != 0 || len(f.Data) > 8192 {
			return errors.New("invalid diagnostic chunk")
		}
		if _, e := base64.StdEncoding.DecodeString(f.Data); e != nil {
			return errors.New("invalid diagnostic encoding")
		}
	case "DONE":
		if f.JIT != "" || f.Data != "" || f.Code < 0 || f.Code > 255 {
			return errors.New("invalid exit code")
		}
	default:
		return errors.New("unknown frame type")
	}
	return nil
}
func Write(w io.Writer, f Frame) error {
	if e := Validate(f); e != nil {
		return e
	}
	b, e := json.Marshal(f)
	if e != nil {
		return e
	}
	b = append(b, '\n')
	if len(b) > MaxFrame {
		return errors.New("frame too large")
	}
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
