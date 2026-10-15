//go:build linux && amd64

package landlockrunner

import (
	"github.com/plover-digital/chickadee/internal/preflight"
	"os"
	"testing"
)

func TestWorkspacePreflightCategories(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	code, _ := preflight.Report(ValidateWorkspace(dir))
	if code != "workspace-permissions" {
		t.Fatal(code)
	}
	code, _ = preflight.Report(ValidateWorkspace(dir + "/missing-parent/slot"))
	if code != "workspace-path" {
		t.Fatalf("missing parent diagnostic: %s", code)
	}
	code, _ = preflight.Report(ValidateWorkspace(dir + "/missing-private-path"))
	if code != "workspace-path" {
		t.Fatal(code)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	w, err := LockWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	code, _ = preflight.Report(func() error {
		other, err := LockWorkspace(dir)
		if other != nil {
			other.Close()
		}
		return err
	}())
	if code != "workspace-busy" {
		t.Fatal(code)
	}
	if err := os.WriteFile(dir+"/private-name", nil, 0600); err != nil {
		t.Fatal(err)
	}
	code, _ = preflight.Report(w.Empty())
	if code != "workspace-not-empty" {
		t.Fatal(code)
	}
}

func TestMissingBubblewrapCategory(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := BubblewrapPath()
	code, _ := preflight.Report(err)
	if code != "bubblewrap-missing" {
		t.Fatal(code)
	}
}
