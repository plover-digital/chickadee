//go:build linux && amd64

package landlockrunner

import (
	"bytes"
	"encoding/json"
	"github.com/plover-digital/chickadee/internal/stateowner"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForeignAndLegacyRuntimeMarkersNeverReap(t *testing.T) {
	for _, kind := range []string{"foreign", "legacy", "invalid-boot"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			if err := stateowner.State(root); err != nil {
				t.Fatal(err)
			}
			who, err := stateowner.Current()
			if err != nil {
				t.Fatal(err)
			}
			owner := who
			version := 3
			switch kind {
			case "foreign":
				owner.Host = strings.Repeat("f", 64)
			case "legacy":
				version = 2
			case "invalid-boot":
				owner.Boot = "not-a-boot"
			}
			dir := filepath.Join(root, "vms", "0123456789abcdef")
			os.MkdirAll(dir, 0700)
			marker := filepath.Join(dir, "sandbox.json")
			raw, _ := json.Marshal(sandboxMarker{Owner: owner, Version: version, Backend: "landlock", ID: "0123456789abcdef", CgroupPath: "/unavailable"})
			os.WriteFile(marker, raw, 0600)
			sentinel := filepath.Join(dir, "live-sentinel")
			os.WriteFile(sentinel, []byte("running"), 0600)
			// A nil cgroup config must never be reached for a foreign host marker.
			if err := CheckOwnership(root); err == nil {
				t.Fatal("unsafe runtime passed preflight")
			}
			if err := ReapWithAccess(root, nil, nil); err == nil {
				t.Fatal("unsafe runtime reaped")
			}
			if got, _ := os.ReadFile(marker); !bytes.Equal(got, raw) {
				t.Fatal("marker changed")
			}
			if got, _ := os.ReadFile(sentinel); string(got) != "running" {
				t.Fatal("runtime changed")
			}
		})
	}
}
func TestSameHostPreviousBootMarkerPassesHostPreflight(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	if err := stateowner.State(root); err != nil {
		t.Fatal(err)
	}
	owner, err := stateowner.Current()
	if err != nil {
		t.Fatal(err)
	}
	owner.Boot = "11111111-1111-1111-1111-111111111111"
	dir := filepath.Join(root, "vms", "0123456789abcdef")
	os.MkdirAll(dir, 0700)
	raw, _ := json.Marshal(sandboxMarker{Owner: owner, Version: 3, Backend: "landlock", ID: "0123456789abcdef"})
	os.WriteFile(filepath.Join(dir, "sandbox.json"), raw, 0600)
	if err := CheckOwnership(root); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceMountPointRequiresStorageAtParent(t *testing.T) {
	if _, err := openWorkspace("/proc"); err == nil || !strings.Contains(err.Error(), "mount storage at its parent") {
		t.Fatalf("mount point was not rejected by the kernel boundary check: %v", err)
	}
}

func TestPreflightWorkspaceLockNeverClaimsUnboundSlots(t *testing.T) {
	parent := t.TempDir()
	os.Chmod(parent, 0700)
	work := filepath.Join(parent, "slot-1")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(parent, ".slot-1.chickadee-owner-v1")
	check := func(dirty bool) {
		t.Helper()
		w, err := lockWorkspaceForCheck(work)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Empty()
		w.Close()
		if (err != nil) != dirty {
			t.Fatalf("dirty=%v empty check=%v", dirty, err)
		}
		if _, err := os.Lstat(claimPath); !os.IsNotExist(err) {
			t.Fatal("read-only preflight created ownership metadata")
		}
	}
	check(false)
	sentinel := filepath.Join(work, "live")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	check(true)
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep" {
		t.Fatal("dirty preflight modified workload data")
	}
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}
	// A failed dirty preflight must not poison a subsequent normal startup.
	w, err := LockWorkspace(work)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if _, err := os.Stat(filepath.Join(claimPath, "identity.json")); err != nil {
		t.Fatal("runtime startup did not durably claim workspace")
	}
}
func TestPreflightForeignWorkspaceRefusesBeforeProbe(t *testing.T) {
	parent := t.TempDir()
	os.Chmod(parent, 0700)
	work := filepath.Join(parent, "slot-1")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	w, err := LockWorkspace(work)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	ownerPath := filepath.Join(parent, ".slot-1.chickadee-owner-v1", "identity.json")
	raw, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	var claim map[string]any
	if err := json.Unmarshal(raw, &claim); err != nil {
		t.Fatal(err)
	}
	claim["identity"].(map[string]any)["host"] = strings.Repeat("f", 64)
	foreign, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownerPath, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(work, "live")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if w, err := lockWorkspaceForCheck(work); err == nil {
		w.Close()
		t.Fatal("foreign slot accepted for scratch probe")
	} else if !strings.Contains(err.Error(), "another host") {
		t.Fatalf("wrong refusal: %v", err)
	}
	if got, err := os.ReadFile(ownerPath); err != nil || !bytes.Equal(got, foreign) {
		t.Fatal("preflight changed foreign claim")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep" {
		t.Fatal("preflight changed live sentinel")
	}
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) != 1 {
		t.Fatal("foreign preflight created scratch data")
	}
}
