package nativeworker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/plover-digital/chickadee/internal/worker"
)

func TestRecoveryRequiresTrustedStopProofAndMatchingNonce(t *testing.T) {
	c := config(t)
	os.MkdirAll(c.StateDir, 0700)
	j, err := worker.Open(filepath.Join(c.StateDir, "journal"), identity(c.Identity))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	q := worker.Request{Identity: identity(c.Identity), AssignmentID: "assignment", VMID: "vm-known", ProfileDigest: c.Digest, CPUs: 2, MemoryMiB: 4096, DiskGiB: 64}
	j.Reserve(q)
	j.Seal(q)
	j.DeliverIntent(q)
	root := filepath.Join(t.TempDir(), "lockroot")
	os.Mkdir(root, 0700)
	os.WriteFile(filepath.Join(root, "vm.lock"), nil, 0600)
	dir := filepath.Join(c.StateDir, "vms", q.VMID)
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "disk.raw"), []byte("owned runtime"), 0600)
	if reconcileAt(c, j, root) == nil {
		t.Fatal("deleted runtime without native exit proof")
	}
	if _, err = os.Stat(filepath.Join(dir, "disk.raw")); err != nil {
		t.Fatal("uncertain disk removed")
	}
	proof, _ := json.Marshal(map[string]any{"v": 1, "vm_id": q.VMID, "nonce": strings.Repeat("a", 32), "stopped": true})
	os.WriteFile(filepath.Join(dir, "native-exit.json"), proof, 0600)
	meta, _ := json.Marshal(map[string]any{"v": 1, "nonce": strings.Repeat("b", 32)})
	os.WriteFile(filepath.Join(dir, "runner-control.json"), meta, 0600)
	if reconcileAt(c, j, root) == nil {
		t.Fatal("mismatched stop proof accepted")
	}
	meta, _ = json.Marshal(map[string]any{"v": 1, "nonce": strings.Repeat("a", 32)})
	os.WriteFile(filepath.Join(dir, "runner-control.json"), meta, 0600)
	if err = reconcileAt(c, j, root); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("proven stopped runtime retained")
	}
	records, _ := j.Records()
	if records[0].State != worker.Terminal {
		t.Fatal("journal capacity not retired")
	}
}
