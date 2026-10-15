//go:build linux && amd64

package host

import (
	"github.com/plover-digital/chickadee/internal/preflight"
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
