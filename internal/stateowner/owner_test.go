//go:build linux

package stateowner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func identity(host, boot string) Identity {
	return Identity{Host: strings.Repeat(host, 64), Boot: boot}
}

var first = identity("a", "11111111-1111-1111-1111-111111111111")
var foreign = identity("b", "22222222-2222-2222-2222-222222222222")

func root(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if e := os.Chmod(p, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestForeignStateRefusalPreservesLiveFiles(t *testing.T) {
	p := root(t)
	if err := state(p, first); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.json", "vms/execution/sandbox.json", "work/sentinel"} {
		f := filepath.Join(p, name)
		os.MkdirAll(filepath.Dir(f), 0700)
		os.WriteFile(f, []byte("live-"+name), 0600)
	}
	before := map[string][]byte{}
	filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if !d.IsDir() {
			before[path], _ = os.ReadFile(path)
		}
		return nil
	})
	if err := state(p, foreign); err == nil {
		t.Fatal("foreign state accepted")
	}
	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("state changed: %s", filepath.Base(path))
		}
	}
	// Same stable host survives a reboot; the original durable claim never changes.
	reboot := first
	reboot.Boot = foreign.Boot
	for _, who := range []Identity{first, reboot} {
		if err := state(p, who); err != nil {
			t.Fatal(err)
		}
	}
	for path, want := range before {
		got, _ := os.ReadFile(path)
		if !bytes.Equal(got, want) {
			t.Fatal("reboot rewrote state")
		}
	}
}
func TestSimultaneousForeignFirstStartupHasOneOwner(t *testing.T) {
	for n := 0; n < 16; n++ {
		p := root(t)
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, who := range []Identity{first, foreign} {
			wg.Add(1)
			go func(i int, who Identity) { defer wg.Done(); <-start; errs[i] = state(p, who) }(i, who)
		}
		close(start)
		wg.Wait()
		winners := 0
		for _, err := range errs {
			if err == nil {
				winners++
			}
		}
		if winners != 1 {
			t.Fatalf("owners=%d errors=%v", winners, errs)
		}
		raw, err := os.ReadFile(filepath.Join(p, "host-owner-v1", "identity.json"))
		if err != nil {
			t.Fatal(err)
		}
		var c claim
		if json.Unmarshal(raw, &c) != nil {
			t.Fatal("claim invalid")
		}
		for _, who := range []Identity{first, foreign} {
			err := state(p, who)
			if (err == nil) != (who.Host == c.Identity.Host) {
				t.Fatal("winner claim changed")
			}
		}
	}
}
func TestUnboundAndIncompleteClaimsFailClosed(t *testing.T) {
	for _, name := range []string{"state.json", "vms", "host-owner-v1"} {
		t.Run(name, func(t *testing.T) {
			p := root(t)
			if name == "state.json" {
				os.WriteFile(filepath.Join(p, name), []byte("live"), 0600)
			} else {
				os.Mkdir(filepath.Join(p, name), 0700)
			}
			if state(p, first) == nil {
				t.Fatal("unsafe legacy or incomplete claim adopted")
			}
			if state(p, first) == nil {
				t.Fatal("partial claim recovered automatically")
			}
		})
	}
}
func TestSharedWorkspaceClaimRefusesDifferentHost(t *testing.T) {
	p := root(t)
	work := filepath.Join(p, "slot-1")
	os.Mkdir(work, 0700)
	claimPath := filepath.Join(p, ".slot-1.chickadee-owner-v1")
	empty := func() error { return nil }
	if err := acquire(claimPath, first, empty); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(work, "live")
	os.WriteFile(sentinel, []byte("running"), 0600)
	if err := acquire(claimPath, foreign, empty); err == nil {
		t.Fatal("different state directory bypassed workspace owner")
	}
	if got, _ := os.ReadFile(sentinel); string(got) != "running" {
		t.Fatal("workspace modified")
	}
}
func TestUnsafeOwnerMetadataRefused(t *testing.T) {
	p := root(t)
	if err := state(p, first); err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(p, "host-owner-v1", "identity.json")
	raw, _ := os.ReadFile(owner)
	os.Remove(owner)
	target := filepath.Join(p, "outside")
	os.WriteFile(target, raw, 0600)
	os.Symlink(target, owner)
	if state(p, first) == nil {
		t.Fatal("symlink claim accepted")
	}
}

func TestOversizedOwnerClaimRejectsTrailingPayload(t *testing.T) {
	p := root(t)
	if err := state(p, first); err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(p, "host-owner-v1", "identity.json")
	raw, err := os.ReadFile(owner)
	if err != nil {
		t.Fatal(err)
	}
	oversized := append(append(raw, bytes.Repeat([]byte(" "), 2048)...), []byte(`{"version":99}`)...)
	if err := os.WriteFile(owner, oversized, 0600); err != nil {
		t.Fatal(err)
	}
	if err := state(p, first); err == nil {
		t.Fatal("oversized claim accepted at decoder limit")
	}
	got, err := os.ReadFile(owner)
	if err != nil || !bytes.Equal(got, oversized) {
		t.Fatal("refusal rewrote invalid claim")
	}
}
