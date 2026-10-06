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
		for i := 0; i < 2; i++ {
			id := host.NewID()
			vm, e := host.StartBootCheck(ctx, c, 1, id)
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
		return nil
	}
	initCtx, cc := context.WithTimeout(ctx, 60*time.Second)
	b, e := github.New(initCtx, c)
	cc()
	if e != nil {
		return e
	}
	if cleanup {
		cleanCtx, cc := context.WithTimeout(ctx, 60*time.Second)
		defer cc()
		return pool.Cleanup(cleanCtx, c, b, owner)
	}
	demand := make(chan int, 16)
	pollErrors := make(chan error, 1)
	poolErrors := make(chan error, 1)
	go func() { pollErrors <- b.Poll(ctx, c.ScaleSet, c.Max, demand) }()
	go func() { poolErrors <- pool.RunOwned(ctx, c, b, demand, owner) }()
	select {
	case e = <-pollErrors:
		cancel()
		<-poolErrors
		return e
	case e = <-poolErrors:
		cancel()
		<-pollErrors
		return e
	}
}
