package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

type Record struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	NotBefore time.Time `json:"not_before,omitempty"`
}

// Persist intent before JIT generation. Names permit reconciliation even when the API response is lost.
func Save(dir string, r Record) error {
	if !idPattern.MatchString(r.ID) {
		return fmt.Errorf("invalid VM ID")
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	p := filepath.Join(dir, "records", r.ID+".json")
	f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	d, e := os.Open(filepath.Dir(p))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func Records(dir string) ([]Record, error) {
	entries, e := os.ReadDir(filepath.Join(dir, "records"))
	if e != nil {
		return nil, e
	}
	var out []Record
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("unexpected journal directory")
		}
		b, e := os.ReadFile(filepath.Join(dir, "records", entry.Name()))
		if e != nil {
			return nil, e
		}
		var r Record
		if json.Unmarshal(b, &r) != nil || !idPattern.MatchString(r.ID) || entry.Name() != r.ID+".json" {
			return nil, fmt.Errorf("invalid journal record")
		}
		out = append(out, r)
	}
	return out, nil
}
func Forget(dir, id string) error { return os.Remove(filepath.Join(dir, "records", id+".json")) }
