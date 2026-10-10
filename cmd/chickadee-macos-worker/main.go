//go:build darwin && arm64

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/plover-digital/chickadee/internal/nativeworker"
	"github.com/plover-digital/chickadee/workerapi"
)

type configuration struct {
	nativeworker.Config
	Listen string             `json:"listen"`
	TLS    workerapi.TLSFiles `json:"tls"`
}

func run(path string, check bool) error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("native worker must be unprivileged")
	}
	i, err := os.Lstat(path)
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 || i.Size() > 128*1024 {
		return fmt.Errorf("private worker config required")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var c configuration
	d := json.NewDecoder(io.LimitReader(f, 128*1024+1))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid native worker config")
	}
	sc := workerapi.ServerConfig{Listen: c.Listen, Identity: c.Identity, TLS: c.TLS}
	if err = workerapi.ValidateServerConfig(sc); err != nil {
		return err
	}
	if check {
		return nativeworker.CheckConfig(c.Config)
	}
	engine, err := nativeworker.OpenEngine(c.Config)
	if err != nil {
		return err
	}
	srv, err := workerapi.NewServer(sc, engine)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- srv.ListenAndServeTLS("", "") }()
	select {
	case <-ctx.Done():
	case <-result:
	}
	engine.Drain(c.Identity)
	wait, cancel := context.WithTimeout(context.Background(), time.Duration(c.JobTimeoutSeconds+180)*time.Second)
	defer cancel()
	err = engine.Wait(wait)
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	srv.Shutdown(shutdown)
	return err
}
func main() {
	path := flag.String("config", "", "Private native worker configuration")
	check := flag.Bool("check", false, "Validate TLS and image without starting VMs/network")
	flag.Parse()
	if run(*path, *check) != nil {
		slog.Error("Native worker stopped; inspect private configuration and lifecycle state")
		os.Exit(1)
	}
}
