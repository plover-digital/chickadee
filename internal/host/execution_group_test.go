//go:build linux && amd64

package host

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRealExecutionGroupKillsDescendantsAndRecovers(t *testing.T) {
	if os.Getenv("CHICKADEE_CGROUP_SMOKE") != "1" {
		t.Skip("requires an explicitly delegated transient unit")
	}
	c := &CgroupConfig{}
	if err := PrepareCgroup(c); err != nil {
		t.Fatal(err)
	}
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "restart"}[recovery], func(t *testing.T) {
			id := NewID()
			group, err := NewExecutionGroup(c, id, 1, 128)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				group.Stop(ctx)
				group.Remove()
			}()
			cmd := exec.Command("/bin/sh", "-c", "sleep 300 & printf 'READY\\n'; wait")
			cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: group.FD()}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			group.CloseFD()
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil || line != "READY\n" {
				t.Fatalf("helper not ready %q %v", line, err)
			}
			procs, err := readControl(group.group.path, "cgroup.procs")
			if err != nil || len(strings.Fields(procs)) < 2 {
				t.Fatalf("missing live descendants %q %v", procs, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if recovery {
				// Simulate a lost guardian: its independently running child survives.
				if err = cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				cmd.Wait()
				state, err := readControl(group.group.path, "cgroup.events")
				if err != nil || populatedZero(state) {
					t.Fatal("descendant did not survive guardian loss", state, err)
				}
				if err = RecoverExecutionGroup(ctx, c, id); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = group.Stop(ctx); err != nil {
					t.Fatal(err)
				}
				cmd.Wait()
				state, err := readControl(group.group.path, "cgroup.events")
				if err != nil || !populatedZero(state) {
					t.Fatal("missing whole-tree exit proof", state, err)
				}
				if err = group.Remove(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = os.Stat(group.group.path); !os.IsNotExist(err) {
				t.Fatal("cgroup retained after proven cleanup", err)
			}
			if err = RecoverExecutionGroup(ctx, c, id); err != nil {
				t.Fatal("recovery not idempotent", err)
			}
		})
	}
}
