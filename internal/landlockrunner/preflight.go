//go:build linux && amd64

package landlockrunner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// CheckSandbox uses the actual child policy without credentials or networking.
func CheckSandbox(image string, diskGiB int) error {
	return CheckSandboxWithAccess(image, diskGiB, nil, 1, 1, nil)
}
func CheckSandboxWithAccess(image string, diskGiB int, access *HostAccess, slot, maxSlots int, protected []string) error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("run the process worker unprivileged")
	}
	if err := CheckPolicyFor(access != nil); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "chickadee-landlock-preflight-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, name := range []string{"passwd", "group"} {
		if err = os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			return err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	plan, err := NormalizeAccess(access, slot, maxSlots, protected)
	if err != nil {
		return err
	}
	var extra []*os.File
	if plan != nil {
		w, err := LockWorkspace(plan.WorkDir)
		if err != nil {
			return err
		}
		defer w.Close()
		if err = w.Empty(); err != nil {
			return err
		}
		extra = append(extra, w.file)
		defer func() {
			for _, f := range extra[1:] {
				f.Close()
			}
		}()
		for _, mount := range plan.Mounts {
			f, _, err := pinMount(mount)
			if err != nil {
				return err
			}
			extra = append(extra, f)
		}
	}
	args, err := sandboxArgsWithAccess(image, exe, dir, diskGiB, true, plan)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	bwrap, err := BubblewrapPath()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bwrap, args...)
	cmd.ExtraFiles = extra
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.Stdout = io.Discard
	var debug bytes.Buffer
	cmd.Stderr = &debug
	if cmd.Run() != nil {
		return fmt.Errorf("Landlock/namespace preflight failed; refusing unsandboxed execution: %.2048s", debug.String())
	}
	return nil
}
