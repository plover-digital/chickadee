//go:build linux && amd64

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	hostconfig "github.com/plover-digital/chickadee/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForeignOwnershipRefusedBeforeJournalRewriteOrRecovery(t *testing.T) {
	c := engineConfig(t)
	j := openTest(t, c.StateDir, c.Identity)
	reserveSeal(t, j, fixture())
	j.Close()
	marker := filepath.Join(c.StateDir, "host-owner-v1", "identity.json")
	raw, _ := os.ReadFile(marker)
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	value["identity"].(map[string]any)["host"] = strings.Repeat("f", 64)
	changed, _ := json.Marshal(value)
	os.WriteFile(marker, changed, 0600)
	journal := filepath.Join(c.StateDir, "state.json")
	before, _ := os.ReadFile(journal)
	c.Identity.Generation++
	_, err := openEngine(c, func(context.Context, hostconfig.Config, int, string) (machine, error) {
		t.Fatal("started foreign execution")
		return nil, nil
	}, func(string) error { t.Fatal("reaped foreign execution"); return nil }, func(Config) error { t.Fatal("preflight touched foreign host resources"); return nil }, unlimitedTestSpace)
	if err == nil {
		t.Fatal("foreign owner accepted")
	}
	after, _ := os.ReadFile(journal)
	if !bytes.Equal(before, after) {
		t.Fatal("foreign journal rewritten")
	}
	got, _ := os.ReadFile(marker)
	if !bytes.Equal(got, changed) {
		t.Fatal("foreign claim overwritten")
	}
}
