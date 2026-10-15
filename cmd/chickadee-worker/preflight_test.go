//go:build linux && amd64

package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/plover-digital/chickadee/internal/preflight"
)

func TestPreflightLogUsesFixedLocalReport(t *testing.T) {
	old := slog.Default()
	defer slog.SetDefault(old)
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	for _, err := range []error{errors.New("PRIVATE_TOKEN /private/config\nFORGED"), fmt.Errorf("PRIVATE_KEY: %w", preflight.New("workspace-permissions"))} {
		logPreflightFailure(err)
	}
	text := output.String()
	for _, unsafe := range []string{"PRIVATE_TOKEN", "PRIVATE_KEY", "/private/config", "FORGED"} {
		if strings.Contains(text, unsafe) {
			t.Fatalf("unsafe check output: %s", unsafe)
		}
	}
	if !strings.Contains(text, "workspace-permissions") || !strings.Contains(text, "mode 0700") || !strings.Contains(text, "config-or-preflight-invalid") {
		t.Fatal("missing bounded actionable report")
	}
}
