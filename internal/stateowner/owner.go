//go:build linux

// Package stateowner fences accidental recovery of another host's local state.
// It is not a distributed lease or a defense against a malicious host administrator.
package stateowner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

var machinePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var hostPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var bootPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Identity struct {
	Host string `json:"host"`
	Boot string `json:"boot"`
}

func (i Identity) Valid() bool {
	return hostPattern.MatchString(i.Host) && bootPattern.MatchString(i.Boot)
}
func Current() (Identity, error) {
	machine, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return Identity{}, fmt.Errorf("host identity unavailable; require a unique /etc/machine-id")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return Identity{}, fmt.Errorf("boot identity unavailable")
	}
	// Files allow one trailing newline, never arbitrary whitespace or configured identity.
	if len(machine) > 0 && machine[len(machine)-1] == '\n' {
		machine = machine[:len(machine)-1]
	}
	if len(boot) > 0 && boot[len(boot)-1] == '\n' {
		boot = boot[:len(boot)-1]
	}
	if !machinePattern.Match(machine) || string(machine) == "00000000000000000000000000000000" || !bootPattern.Match(boot) {
		return Identity{}, fmt.Errorf("invalid host or boot identity")
	}
	sum := sha256.Sum256(append([]byte("chickadee-host-v1:"), machine...))
	return Identity{Host: hex.EncodeToString(sum[:]), Boot: string(boot)}, nil
}

type claim struct {
	Version  int      `json:"version"`
	Identity Identity `json:"identity"`
}

// State must be claimed before opening journals, taking local locks, or recovery.
func State(root string) error {
	who, err := Current()
	if err != nil {
		return err
	}
	return state(root, who)
}
func state(root string, who Identity) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	if err := private(root, true); err != nil {
		return err
	}
	return acquire(filepath.Join(root, "host-owner-v1"), who, func() error {
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Name() != "host-owner-v1" {
				return fmt.Errorf("unbound existing state; drain all controllers and migrate to a fresh state directory")
			}
		}
		return nil
	})
}

// Workspace claims live beside the guest-writable directory, never inside it.
// Different state directories therefore cannot bypass foreign-host fencing.
func Workspace(path string) error {
	who, err := Current()
	if err != nil {
		return err
	}
	return acquire(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".chickadee-owner-v1"), who, func() error {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("unbound nonempty workspace; drain all controllers and migrate to a fresh workspace")
		}
		return nil
	})
}
func private(path string, dir bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 || (dir && !info.IsDir()) || (!dir && !info.Mode().IsRegular()) {
		return fmt.Errorf("host owner metadata must be private, direct and service-owned")
	}
	return nil
}
func acquire(path string, who Identity, fresh func() error) error {
	err := os.Mkdir(path, 0700)
	if err == nil {
		// mkdir is the cross-host atomic election. Never remove an incomplete claim:
		// another observer must refuse, not adopt after an arbitrary timeout.
		if err = fresh(); err != nil {
			return err
		}
		b, _ := json.Marshal(claim{Version: 1, Identity: who})
		f, e := os.OpenFile(filepath.Join(path, "identity.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
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
		for _, p := range []string{path, filepath.Dir(path)} {
			d, e := os.Open(p)
			if e != nil {
				return e
			}
			e = d.Sync()
			d.Close()
			if e != nil {
				return e
			}
		}
		return nil
	}
	if !os.IsExist(err) {
		return err
	}
	if err = private(path, true); err != nil {
		return err
	}
	p := filepath.Join(path, "identity.json")
	if err = private(p, false); err != nil {
		return fmt.Errorf("incomplete or unsafe host owner claim; retain state for operator inspection")
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 1025))
	dec.DisallowUnknownFields()
	var got claim
	if dec.Decode(&got) != nil || dec.Decode(new(any)) != io.EOF || got.Version != 1 || !got.Identity.Valid() {
		return fmt.Errorf("invalid host owner claim; retain state")
	}
	if got.Identity.Host != who.Host {
		return fmt.Errorf("state belongs to another host; stop duplicate controllers and use node-local state and workspaces")
	}
	return nil
}
