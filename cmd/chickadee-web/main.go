package main

import (
	"context"
	"errors"
	"github.com/plover-digital/chickadee/internal/site"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(closeCtx)
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
