//go:build linux && amd64

package main

import (
	"context"
	"errors"
	"flag"
	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/github"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/pool"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	path := flag.String("config", "/etc/chickadee/config.json", "configuration path")
	check := flag.Bool("check", false, "validate configuration without contacting GitHub")
	cleanup := flag.Bool("cleanup", false, "recover local state and remove stale registrations without starting runners")
	bootCheck := flag.Bool("boot-check", false, "boot to READY twice and destroy both VMs; no GitHub or host network changes")
	flag.Parse()
	if (*check && (*cleanup || *bootCheck)) || (*cleanup && *bootCheck) {
		slog.Error("choose only one of -check, -cleanup, or -boot-check")
		os.Exit(1)
	}
	if *check {
		if _, e := config.Load(*path); e != nil {
			slog.Error("invalid configuration", "reason", e.Error())
			os.Exit(1)
		}
		return
	}
	if e := run(*path, *cleanup, *bootCheck); e != nil && !errors.Is(e, context.Canceled) {
		slog.Error("controller stopped", "reason", e.Error())
		os.Exit(1)
	}
}
func run(path string, cleanup, bootCheck bool) error {
	if os.Geteuid() == 0 {
		return errors.New("run chickadee as the unprivileged service account; use root only for installation and network setup")
	}
	c, e := config.Load(path)
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	owner, e := pool.Acquire(c)
	if e != nil {
		return e
	}
	defer owner.Close()
	if bootCheck {
		for _, profile := range c.ProfileConfigs() {
			for i := 0; i < 2; i++ {
				id := host.NewID()
				vm, e := host.StartBootCheck(ctx, profile, 1, id)
				if e != nil {
					if vm != nil {
						if ce := vm.Cleanup(); ce != nil {
							return ce
						}
					}
					return e
				}
				slog.Info("boot check READY", "vm", id)
				if e = vm.Cleanup(); e != nil {
					return e
				}
				slog.Info("boot check QEMU exited and disk deleted", "vm", id)
			}
		}
		return nil
	}

	profiles := c.ProfileConfigs()
	backends := map[string]pool.Backend{}
	clients := map[string]*github.Client{}
	for _, p := range profiles {
		initCtx, cc := context.WithTimeout(ctx, 60*time.Second)
		b, e := github.New(initCtx, p)
		cc()
		if e != nil {
			return e
		}
		backends[p.Key()] = b
		clients[p.Key()] = b
	}
	records, e := host.Records(c.StateDir)
	if e != nil {
		return e
	}
	for _, r := range records {
		name, e := pool.RecordProfile(c, r)
		if e != nil {
			return e
		}
		if backends[name] != nil {
			continue
		}
		recovery, e := pool.RecoveryConfig(c, r)
		if e != nil {
			return e
		}
		initCtx, cc := context.WithTimeout(ctx, 60*time.Second)
		b, e := github.Existing(initCtx, recovery)
		cc()
		if e != nil {
			return e
		}
		backends[name] = b
	}
	if cleanup {
		cleanCtx, cc := context.WithTimeout(ctx, 60*time.Second)
		defer cc()
		return pool.CleanupProfiles(cleanCtx, c, backends, owner)
	}
	return serve(ctx, c, path, backends, clients, owner)
}
