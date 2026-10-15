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
