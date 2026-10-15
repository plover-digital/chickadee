//go:build linux && amd64

package host

import (
	"github.com/plover-digital/chickadee/internal/preflight"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParentMemoryPreflightCategories(t *testing.T) {
	for value, want := range map[string]string{"max": "cgroup-memory-unbounded", "1": "cgroup-memory-small", "invalid": "cgroup-memory-small"} {
		code, _ := preflight.Report(checkParentMemory(value, 4096))
		if code != want {
			t.Fatalf("%s: %s", value, code)
		}
	}
	if err := checkParentMemory("4096", 4096); err != nil {
		t.Fatal(err)
	}
}

func TestVMProbeFailuresUseFixedCategories(t *testing.T) {
	path := t.TempDir()
	t.Setenv("PATH", path)
	code, _ := preflight.Report(runSandboxProbe(nil))
	if code != "bubblewrap-missing" {
		t.Fatal(code)
	}
	bwrap := filepath.Join(path, "bwrap")
	if err := os.WriteFile(bwrap, []byte("#!/bin/sh\nprintf 'PRIVATE_PROBE_ERROR /private/config' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	err := runSandboxProbe(nil)
	code, remedy := preflight.Report(err)
	if code != "sandbox-probe-failed" {
		t.Fatal(code)
	}
	if strings.Contains(err.Error()+remedy, "PRIVATE_PROBE_ERROR") || strings.Contains(err.Error()+remedy, "/private/config") {
		t.Fatal("probe output leaked")
	}
}
