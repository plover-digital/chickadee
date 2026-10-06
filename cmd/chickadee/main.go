//go:build linux && amd64

package main

import (
	"context"
	"errors"
	"flag"
	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/github"
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
	flag.Parse()
	if *check {
		if _, e := config.Load(*path); e != nil {
			slog.Error("invalid configuration", "reason", e.Error())
			os.Exit(1)
		}
		return
	}
	if e := run(*path, *cleanup); e != nil && !errors.Is(e, context.Canceled) {
		slog.Error("controller stopped", "reason", e.Error())
		os.Exit(1)
	}
}
func run(path string, cleanup bool) error {
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
