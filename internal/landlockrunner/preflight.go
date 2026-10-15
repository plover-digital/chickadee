//go:build linux && amd64

package landlockrunner

import (
	"context"
	"fmt"
	"github.com/plover-digital/chickadee/internal/preflight"
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
	var workspace *Workspace
	var extra []*os.File
	if plan != nil {
		w, err := LockWorkspace(plan.WorkDir)
		if err != nil {
			return err
		}
		defer w.Close()
		workspace = w
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
	cmd.Stderr = io.Discard
	runErr := cmd.Run()
	if workspace != nil {
		// The probe has no descendants; bwrap has exited before anchored cleanup.
		if err := workspace.Verify(); err != nil {
			return err
		}
		if err := workspace.root.Remove(".chickadee-probe"); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("workspace probe cleanup failed")
		}
		if err := workspace.Empty(); err != nil {
			return err
		}
	}
	if runErr != nil {
		return preflight.New("sandbox-probe-failed")
	}
	return nil
}
