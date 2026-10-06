//go:build linux && amd64

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/plover-digital/chickadee/internal/config"
)

func TestQueueReloadAcknowledgesExactConfigAndRejectsHostChanges(t *testing.T) {
	raw, e := os.ReadFile("../../examples/scopes.json")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	current, e := config.Load(path)
	if e != nil {
		t.Fatal(e)
	}
	var candidate map[string]any
	if e = json.Unmarshal(raw, &candidate); e != nil {
		t.Fatal(e)
	}
	scopes := candidate["scopes"].(map[string]any)
	scopes["new-customer"] = map[string]any{"github_url": "https://github.com/test-owner/private-repo", "app_installation_id": 999, "runner_group_id": 1, "max_vms": 1, "profiles": map[string]any{"chickadee": map[string]any{"image": "ubuntu-2604", "resources": "medium", "warm_pool": 0, "max_vms": 1}}}
	updated, _ := json.Marshal(candidate)
	os.WriteFile(path, updated, 0600)
	next, digest, e := loadQueueConfig(path, current)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(updated)
	if digest != hex.EncodeToString(sum[:]) || len(next.Scopes) != len(current.Scopes)+1 {
		t.Fatal("reload did not match exact added scope")
	}
	dir := t.TempDir()
	if e = writeReloadResult(dir, digest, nil); e != nil {
		t.Fatal(e)
	}
	ack := filepath.Join(dir, "reload.json")
	info, e := os.Stat(ack)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("reload acknowledgement must be private")
	}
	var state map[string]any
	data, _ := os.ReadFile(ack)
	json.Unmarshal(data, &state)
	if state["config_sha256"] != digest || state["status"] != "applied" {
		t.Fatal("incorrect reload acknowledgement")
	}
	candidate["limits"].(map[string]any)["max_vcpus"] = 5
	rejected, _ := json.Marshal(candidate)
	os.WriteFile(path, rejected, 0600)
	_, rejectedDigest, e := loadQueueConfig(path, next)
	if e == nil {
		t.Fatal("host resource change accepted during live queue reload")
	}
	if e = writeReloadResult(dir, rejectedDigest, e); e != nil {
		t.Fatal(e)
	}
	data, _ = os.ReadFile(ack)
	json.Unmarshal(data, &state)
	if state["status"] != "rejected" || state["config_sha256"] != rejectedDigest {
		t.Fatal("rejected update was not distinguishable from previous acknowledgement")
	}
}
