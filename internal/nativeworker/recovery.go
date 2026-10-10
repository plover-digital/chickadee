package nativeworker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/plover-digital/chickadee/internal/worker"
)

// OpenEngine verifies the local base once and starts shared credential-free warm
// capacity. This implementation accepts exactly one small native Mac profile.
func OpenEngine(c Config) (*Engine, error) {
	if err := CheckConfig(c); err != nil {
		return nil, err
	}
	return open(c, func(id string) (session, error) { return startProcess(c, id) }, func(j *worker.Journal) error { return reconcile(c, j) })
}

// reconcile never kills a PID from persistent state. Parent-lifetime EOF stops
// orphan helpers; a trusted native stop proof plus exclusive VM lock is required
// before removing writable files. Missing proof retains capacity/admission.
func reconcile(c Config, j *worker.Journal) error {
	root := filepath.Join(os.Getenv("HOME"), ".local", "state", "chickadee-macos")
	if !filepath.IsAbs(root) {
		return fmt.Errorf("private worker home required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(root, "vm.lock"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		file.Close()
	} else if !os.IsExist(err) {
		return err
	}
	return reconcileAt(c, j, root)
}
func reconcileAt(c Config, j *worker.Journal, root string) error {
	if e := ownedPrivate(root, true); e != nil {
		return e
	}
	path := filepath.Join(root, "vm.lock")
	if e := ownedPrivate(path, false); e != nil {
		return e
	}
	fd, e := syscall.Open(path, syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	defer syscall.Close(fd)
	if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return e
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	vmroot := filepath.Join(c.StateDir, "vms")
	logs := filepath.Join(vmroot, "logs")
	if e = os.MkdirAll(logs, 0700); e != nil {
		return e
	}
	if e = ownedPrivate(vmroot, true); e != nil {
		return e
	}
	if e = ownedPrivate(logs, true); e != nil {
		return e
	}
	records, e := j.Records()
	if e != nil {
		return e
	}
	proofFor := func(id string) error {
		var proof struct {
			V       int    `json:"v"`
			VMID    string `json:"vm_id"`
			Nonce   string `json:"nonce"`
			Stopped bool   `json:"stopped"`
		}
		dir := filepath.Join(vmroot, id)
		file := filepath.Join(dir, "native-exit.json")
		if _, err := os.Lstat(dir); os.IsNotExist(err) {
			file = filepath.Join(logs, id+"-native-exit.json")
		}
		if err := ownedPrivate(file, false); err != nil {
			return fmt.Errorf("native stop proof unavailable")
		}
		data, err := os.ReadFile(file)
		if err != nil || len(data) > 4096 || json.Unmarshal(data, &proof) != nil || proof.V != 1 || proof.VMID != id || !proof.Stopped || len(proof.Nonce) != 32 {
			return fmt.Errorf("invalid native stop proof")
		}
		if _, err = os.Lstat(dir); err == nil {
			if err = ownedPrivate(dir, true); err != nil {
				return err
			}
			metadata := filepath.Join(dir, "runner-control.json")
			if err = ownedPrivate(metadata, false); err != nil {
				return err
			}
			raw, err := os.ReadFile(metadata)
			if err != nil || len(raw) > 4096 {
				return fmt.Errorf("invalid recovery metadata")
			}
			var m struct {
				V     int    `json:"v"`
				Nonce string `json:"nonce"`
			}
			if json.Unmarshal(raw, &m) != nil || m.V != 1 || m.Nonce != proof.Nonce {
				return fmt.Errorf("native stop proof correlation mismatch")
			}
			if err = os.RemoveAll(dir); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	for _, r := range records {
		if r.State != worker.Terminal {
			if e = proofFor(r.Request.VMID); e != nil {
				return e
			}
			if _, e = j.RecoverTerminal(r.Request.AssignmentID, worker.ExitProof{VMID: r.Request.VMID, ProcessExitConfirmed: true, DiskRemoved: true}); e != nil {
				return e
			}
		}
	}
	entries, e := os.ReadDir(vmroot)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.Name() == "logs" {
			continue
		}
		if !idPattern.MatchString(entry.Name()) || !entry.IsDir() {
			return fmt.Errorf("unknown native runtime state")
		}
		if e = proofFor(entry.Name()); e != nil {
			return e
		}
	}
	return nil
}
