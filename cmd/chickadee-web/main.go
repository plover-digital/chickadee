package main

import (
	"context"
	"errors"
	"github.com/plover-digital/chickadee/internal/site"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	id, _ := strconv.ParseInt(os.Getenv("CHICKADEE_APP_ID"), 10, 64)
	c := site.Config{PublicURL: env("CHICKADEE_PUBLIC_URL", "http://127.0.0.1:8080"), AppSlug: env("CHICKADEE_APP_SLUG", "chickadee-run"), ClientID: os.Getenv("CHICKADEE_APP_CLIENT_ID"), AppID: id, StateDir: env("CHICKADEE_WEB_STATE", "/var/lib/chickadee-web")}
	if path := os.Getenv("CHICKADEE_OAUTH_SECRET_FILE"); path != "" {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			slog.Error("OAuth secret file must be private and regular")
			os.Exit(1)
		}
		b, e := os.ReadFile(path)
		if e != nil {
			slog.Error("OAuth secret unavailable")
			os.Exit(1)
		}
		c.ClientSecret = strings.TrimSpace(string(b))
		if c.ClientSecret == "" {
			slog.Error("OAuth secret file is empty")
			os.Exit(1)
		}
	}
	handler, e := site.New(c)
	if e != nil {
		slog.Error("site configuration invalid", "reason", e.Error())
		os.Exit(1)
	}
	server := &http.Server{Addr: "127.0.0.1:8080", Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	var admin *http.Server
	if os.Getenv("CHICKADEE_WEB_ADMIN") == "1" {
		path := filepath.Join(c.StateDir, "admin.sock")
		if info, err := os.Lstat(path); err == nil {
			if info.Mode()&os.ModeSocket == 0 {
				slog.Error("admin socket path is not a socket")
				os.Exit(1)
			}
			conn, err := net.DialTimeout("unix", path, time.Second)
			if err == nil {
				conn.Close()
				slog.Error("admin socket already in use")
				os.Exit(1)
			}
			if err = os.Remove(path); err != nil {
				slog.Error("stale admin socket could not be removed")
				os.Exit(1)
			}
		} else if !os.IsNotExist(err) {
			slog.Error("admin socket unavailable")
			os.Exit(1)
		}
		listener, err := net.Listen("unix", path)
		if err != nil {
			slog.Error("admin socket unavailable")
			os.Exit(1)
		}
		if err = os.Chmod(path, 0600); err != nil {
			listener.Close()
			slog.Error("admin socket permissions unavailable")
			os.Exit(1)
		}
		admin = &http.Server{Handler: handler.AdminHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
		go func() {
			if err := admin.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("admin socket stopped")
				_ = server.Close()
			}
		}()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(closeCtx)
		if admin != nil {
			_ = admin.Shutdown(closeCtx)
		}
	}()
	slog.Info("onboarding site listening", "address", server.Addr)
	if e = server.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
		slog.Error("site stopped")
		os.Exit(1)
	}
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
