//go:build linux && amd64

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/plover-digital/chickadee/internal/github"
	"github.com/plover-digital/chickadee/internal/worker"
	"log/slog"
	"os"
	"path/filepath"
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
		logPreflightFailure(err, "")
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

func TestCheckLogPreservesGitHubCredentialCategory(t *testing.T) {
	root := t.TempDir()
	credential := filepath.Join(root, "private-credential")
	if err := os.WriteFile(credential, []byte("PRIVATE_CREDENTIAL_CONTENT"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(credential, 0644); err != nil {
		t.Fatal(err)
	}
	old := slog.Default()
	defer slog.SetDefault(old)
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	for _, pat := range []bool{false, true} {
		g := &github.StandaloneConfig{GitHubURL: "https://github.com/example/repository", ScaleSet: "sandbox", RunnerGroupID: 1, ClientID: "fixture", InstallationID: 1, KeyFile: credential}
		want := "app-key-unreadable"
		if pat {
			g.ClientID = ""
			g.InstallationID = 0
			g.KeyFile = ""
			g.TokenFile = credential
			want = "token-file-unreadable"
		}
		c := configuration{Config: worker.Config{Profiles: []worker.Profile{{Machine: "landlock"}}}, GitHub: g}
		encoded, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		config := filepath.Join(root, "private-config.json")
		if err = os.WriteFile(config, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		_, err = load(config)
		if err == nil {
			t.Fatal("unsafe credential fixture accepted")
		}
		output.Reset()
		logPreflightFailure(err, "")
		text := output.String()
		if !strings.Contains(text, want) {
			t.Fatalf("lost credential diagnostic: %s", text)
		}
		if strings.Contains(text, root) || strings.Contains(text, "PRIVATE_CREDENTIAL_CONTENT") {
			t.Fatal("credential or path leaked")
		}
	}
}

func TestGitHubCheckKeepsConfigStageWithoutRawErrors(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private-config.json")
	if err := os.WriteFile(path, []byte("{PRIVATE_CONFIG_CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := load(path)
	if err == nil {
		t.Fatal("malformed config accepted")
	}
	old := slog.Default()
	defer slog.SetDefault(old)
	var out bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	logPreflightFailure(err, "config-invalid")
	if !strings.Contains(out.String(), `"reason":"config-invalid"`) {
		t.Fatal("lost safe config-stage reason")
	}
	if strings.Contains(out.String(), root) || strings.Contains(out.String(), "PRIVATE_CONFIG_CONTENT") {
		t.Fatal("config leaked")
	}
}
