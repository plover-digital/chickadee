//go:build linux && amd64

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/landlockrunner"
	"github.com/plover-digital/chickadee/internal/protocol"
	"github.com/plover-digital/chickadee/internal/worker"
)

// runJITOnce provides standalone operation without Roost, an API listener or App
// credentials. The caller supplies one fresh JIT configuration over private stdin.
func runJITOnce(path string) error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("run the process worker unprivileged")
	}
	c, err := load(path)
	if err != nil {
		return err
	}
	if !c.IsLandlock() || len(c.Profiles) != 1 || c.Budget.MaxVMs != 1 {
		return fmt.Errorf("one-shot mode requires one Landlock profile and one slot")
	}
	if err = worker.CheckConfig(c.Config); err != nil {
		return err
	}
	if c.Profiles[0].HostAccess == nil {
		if err = landlockrunner.CheckSandbox(c.Profiles[0].ImageDir, c.Profiles[0].DiskGiB); err != nil {
			return err
		}
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, protocol.MaxJIT+2))
	if err != nil {
		return err
	}
	jit := strings.TrimSpace(string(data))
	if err = protocol.Validate(protocol.Frame{V: 1, Type: "CONFIG", JIT: jit}); err != nil {
		return fmt.Errorf("invalid bounded JIT input")
	}
	engine, err := worker.OpenEngine(c.Config)
	if err != nil {
		return err
	}
	defer func() {
		engine.Drain(c.Identity)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.BootTimeoutSeconds+c.JobTimeoutSeconds+180)*time.Second)
		defer cancel()
		engine.Wait(ctx)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.BootTimeoutSeconds+5)*time.Second)
	defer cancel()
	assignment := host.NewID()
	p := c.Profiles[0]
	if _, err = engine.Reserve(ctx, c.Identity, assignment, p.ID); err != nil {
		return err
	}
	if _, err = engine.Seal(c.Identity, assignment); err != nil {
		return err
	}
	if err = engine.Deliver(c.Identity, assignment, jit); err != nil {
		return err
	}
	engine.Drain(c.Identity)
	done, cancelDone := context.WithTimeout(context.Background(), time.Duration(c.JobTimeoutSeconds+180)*time.Second)
	defer cancelDone()
	if err = engine.Wait(done); err != nil {
		return err
	}
	// Cleanup completion is not an assertion that the GitHub job passed.
	fmt.Println("RUNNER_RETIRED")
	return nil
}
