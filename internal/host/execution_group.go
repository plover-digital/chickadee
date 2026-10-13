//go:build linux && amd64

package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExecutionGroup supplies mandatory pre-exec accounting and kernel-confirmed
// process-tree cleanup for the process runner. It never relies on stored PIDs.
type ExecutionGroup struct{ group *vmCgroup }

func NewExecutionGroup(c *CgroupConfig, id string, cpus, memoryMiB int) (*ExecutionGroup, error) {
	if c == nil {
		return nil, fmt.Errorf("process runners require cgroup delegation")
	}
	g, err := createCgroup(c, id, cpus, memoryMiB)
	if g == nil {
		return nil, err
	}
	return &ExecutionGroup{g}, err
}
func (g *ExecutionGroup) FD() int  { return int(g.group.file.Fd()) }
func (g *ExecutionGroup) CloseFD() { g.group.closeFD() }
func (g *ExecutionGroup) Stop(ctx context.Context) error {
	if err := writeControl(g.group.path, "cgroup.kill", "1"); err != nil {
		return err
	}
	for {
		state, err := readControl(g.group.path, "cgroup.events")
		if err != nil {
			return err
		}
		if populatedZero(state) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("execution cgroup still populated")
		case <-time.After(25 * time.Millisecond):
		}
	}
}
func populatedZero(value string) bool {
	for _, line := range strings.Split(value, "\n") {
		if line == "populated 0" {
			return true
		}
	}
	return false
}
func (g *ExecutionGroup) Remove() error { g.CloseFD(); return removeEmptyCgroup(g.group.path) }

func ExecutionGroupPath(c *CgroupConfig, id string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("process runner cgroup required")
	}
	root, err := delegatedRoot(c)
	if err != nil {
		return "", err
	}
	name := "chickadee-vm-" + id
	if !ownedCgroup.MatchString(name) {
		return "", fmt.Errorf("invalid execution identity")
	}
	return filepath.Join(root, name), nil
}

// RecoverExecutionGroup acts only on an exact owned name under this service's
// delegated subtree. The caller must hold its journal lock and validate the
// matching private runtime marker before calling it.
func RecoverExecutionGroup(ctx context.Context, c *CgroupConfig, id string) error {
	path, err := ExecutionGroupPath(c, id)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	g := &ExecutionGroup{&vmCgroup{path: path}}
	if err = g.Stop(ctx); err != nil {
		return err
	}
	return g.Remove()
}
