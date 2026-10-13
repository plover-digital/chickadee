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
	if os.Geteuid() == 0 {
		return fmt.Errorf("run the process worker unprivileged")
	}
	if err := CheckPolicy(); err != nil {
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
	args, err := sandboxArgs(image, exe, dir, diskGiB, true)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bwrap", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.Stdout = io.Discard
	var debug bytes.Buffer
	cmd.Stderr = &debug
	if cmd.Run() != nil {
		return fmt.Errorf("Landlock/namespace preflight failed; refusing unsandboxed execution: %.2048s", debug.String())
	}
	return nil
}
