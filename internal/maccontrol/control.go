// Package maccontrol implements the experimental bounded macOS block transport.
// It is separate from serial protocol v1 and is not part of the worker API.
package maccontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

const (
	Page          = 4096
	DiskBytes     = 1024 * 1024
	ConfigOffset  = 65536
	PayloadOffset = ConfigOffset + Page
	MaxPayload    = 61440
	LogsOffset    = 131072
	MaxLogs       = 512 * 1024
	MaxJIT        = 48 * 1024
)

type Header struct {
	V       int    `json:"v"`
	Kind    string `json:"kind"`
	Nonce   string `json:"nonce"`
	Network bool   `json:"network"`
}
type Commit struct {
	V      int    `json:"v"`
	Nonce  string `json:"nonce"`
	Length int    `json:"length"`
	SHA256 string `json:"sha256"`
}
type Config struct {
	V     int    `json:"v"`
	Nonce string `json:"nonce"`
	Type  string `json:"type"`
	JIT   string `json:"jit"`
}
type Status struct {
	V         int    `json:"v"`
	Nonce     string `json:"nonce"`
	Type      string `json:"type"`
	Sequence  int    `json:"sequence"`
	Code      int    `json:"code"`
	LogSize   int    `json:"log_size"`
	LogSHA256 string `json:"log_sha256"`
}

var noncePattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func Decode(data []byte, target any) error {
	if len(data) > MaxPayload {
		return errors.New("oversize frame")
	}
	data = bytes.TrimRight(data, "\x00")
	// Reject duplicate keys before decoding into typed structs.
	keys := json.NewDecoder(bytes.NewReader(data))
	if t, err := keys.Token(); err != nil || t != json.Delim('{') {
		return errors.New("invalid object")
	}
	seen := map[string]bool{}
	for keys.More() {
		key, err := keys.Token()
		if err != nil {
			return errors.New("invalid field")
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return errors.New("duplicate field")
		}
		seen[name] = true
		var value json.RawMessage
		if keys.Decode(&value) != nil {
			return errors.New("invalid field value")
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid JSON")
	}
	return nil
}

func ParseHeader(page []byte) (Header, error) {
	var h Header
	if len(page) != Page || Decode(page, &h) != nil || h.V != 1 || h.Kind != "chickadee-runner-control" || !noncePattern.MatchString(h.Nonce) {
		return Header{}, errors.New("invalid runner header")
	}
	return h, nil
}

func ReadConfig(reader io.ReaderAt, nonce string) (Config, error) {
	page := make([]byte, Page)
	var commit Commit
	if _, err := reader.ReadAt(page, ConfigOffset); err != nil || Decode(page, &commit) != nil || commit.V != 1 || commit.Nonce != nonce || commit.Length < 1 || commit.Length > MaxPayload || !hashPattern.MatchString(commit.SHA256) {
		return Config{}, errors.New("configuration absent or incomplete")
	}
	// macOS raw block devices require sector-aligned I/O lengths. Read the
	// complete fixed payload page region, then hash only the committed bytes.
	payload := make([]byte, MaxPayload)
	if _, err := reader.ReadAt(payload, PayloadOffset); err != nil {
		return Config{}, errors.New("incomplete configuration")
	}
	payload = payload[:commit.Length]
	h := sha256.Sum256(payload)
	if hex.EncodeToString(h[:]) != commit.SHA256 {
		return Config{}, errors.New("configuration checksum mismatch")
	}
	var config Config
	if Decode(payload, &config) != nil || config.V != 1 || config.Nonce != nonce || config.Type != "CONFIG" || len(config.JIT) == 0 || len(config.JIT) > MaxJIT {
		return Config{}, errors.New("invalid configuration")
	}
	if _, err := base64.StdEncoding.DecodeString(config.JIT); err != nil {
		return Config{}, errors.New("invalid JIT encoding")
	}
	return config, nil
}

func WriteStatus(writer io.WriterAt, status Status) error {
	if status.V != 1 || !noncePattern.MatchString(status.Nonce) || status.Code < 0 || status.Code > 255 || status.LogSize < 0 || status.LogSize > MaxLogs {
		return errors.New("invalid status")
	}
	expected := map[string]int{"READY": 0, "ACK": 1, "RUNNER_STARTED": 2, "EXIT": 3}
	seq, ok := expected[status.Type]
	if !ok || status.Sequence != seq {
		return errors.New("invalid status sequence")
	}
	if status.Type != "EXIT" && (status.Code != 0 || status.LogSize != 0 || status.LogSHA256 != "") {
		return errors.New("unexpected status data")
	}
	if status.LogSize > 0 && !hashPattern.MatchString(status.LogSHA256) {
		return errors.New("invalid log checksum")
	}
	payload, err := json.Marshal(status)
	if err != nil || len(payload) > Page {
		return errors.New("invalid status encoding")
	}
	page := make([]byte, Page)
	copy(page, payload)
	_, err = writer.WriteAt(page, Page)
	return err
}
