package github

import (
	"context"
	"errors"
	"fmt"
	"github.com/actions/scaleset"
	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/workerapi"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// startupFailure contains only a fixed code, never an SDK response or credential.
type startupFailure string

func (e startupFailure) Error() string { return string(e) }
func FailureCategory(err error) string {
	var stage startupFailure
	if errors.As(err, &stage) {
		return string(stage)
	}
	return ""
}

type Client struct {
	API   *scaleset.Client
	SetID int
}

// Actions cache archives paths relative to GITHUB_WORKSPACE. Match the hosted
// Linux workspace layout so caches outside the checkout (for example Bun in
// HOME) restore to the same location after switching between runner providers.
const runnerWorkFolder = "/home/runner/work"

func New(ctx context.Context, c config.Config) (*Client, error) { return newClient(ctx, c, true) }

// Existing never recreates a removed scale set during journal recovery.
func Existing(ctx context.Context, c config.Config) (*Client, error) { return newClient(ctx, c, false) }
func newClient(ctx context.Context, c config.Config, create bool) (*Client, error) {
	if e := validateScope(c.GitHubURL, c.GitHubHost); e != nil {
		return nil, e
	}
	credential, pat, e := credentialFile(c)
	if e != nil {
		return nil, e
	}
	// Do not replay non-idempotent JIT creation after an ambiguous HTTP failure.
	opts := []scaleset.HTTPOption{scaleset.WithTimeout(65 * time.Second), scaleset.WithRetryMax(0)}
	system := scaleset.SystemInfo{System: "chickadee", Version: "prototype"}
	var api *scaleset.Client
	if pat {
		api, e = scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{GitHubConfigURL: c.GitHubURL, PersonalAccessToken: credential, SystemInfo: system}, opts...)
	} else {
		api, e = scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{GitHubConfigURL: c.GitHubURL, GitHubAppAuth: scaleset.GitHubAppAuth{ClientID: c.ClientID, InstallationID: c.InstallationID, PrivateKey: credential}, SystemInfo: system}, opts...)
	}
	if e != nil {
		return nil, startupFailure("github-auth-failed")
	}
	set, e := api.GetRunnerScaleSet(ctx, c.RunnerGroupID, c.ScaleSet)
	if e != nil {
		return nil, startupFailure("scale-set-lookup-failed")
	}
	if set == nil && !create {
		return &Client{API: api}, nil
	}
	if set == nil {
		set, e = api.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{Name: c.ScaleSet, RunnerGroupID: c.RunnerGroupID, RunnerSetting: scaleset.RunnerSetting{DisableUpdate: true}})
		if e != nil {
			return nil, startupFailure("scale-set-create-failed")
		}
	}
	if !set.RunnerSetting.DisableUpdate {
		return nil, startupFailure("scale-set-policy-invalid")
	}
	return &Client{API: api, SetID: set.ID}, nil
}

// credentialFile rejects mixed auth and never returns raw file errors.
func credentialPath(c config.Config) (string, bool, error) {
	pat := c.TokenFile != ""
	path, category := c.KeyFile, "app-key-unreadable"
	if pat {
		if c.KeyFile != "" || c.ClientID != "" || c.InstallationID != 0 {
			return "", false, startupFailure("config-invalid")
		}
		u, err := url.Parse(c.GitHubURL)
		if err != nil || len(strings.Split(strings.Trim(u.Path, "/"), "/")) != 2 {
			return "", false, startupFailure("config-invalid")
		}
		path, category = c.TokenFile, "token-file-unreadable"
	} else if c.ClientID == "" || c.InstallationID <= 0 {
		return "", false, startupFailure("config-invalid")
	}
	if !filepath.IsAbs(path) {
		return "", false, startupFailure("config-invalid")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || (info.Mode().Perm()&0077 != 0 || (pat && info.Mode().Perm() != 0600)) || info.Size() > 64*1024 {
		return "", pat, startupFailure(category)
	}
	return path, pat, nil
}
func credentialFile(c config.Config) (string, bool, error) {
	path, pat, err := credentialPath(c)
	if err != nil {
		return "", pat, err
	}
	category := "app-key-unreadable"
	if pat {
		category = "token-file-unreadable"
	}
	f, err := os.Open(path)
	if err != nil {
		return "", pat, startupFailure(category)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || (info.Mode().Perm()&0077 != 0 || (pat && info.Mode().Perm() != 0600)) || info.Size() > 64*1024 {
		return "", pat, startupFailure(category)
	}
	data, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil || len(data) == 0 || len(data) > 64*1024 {
		return "", pat, startupFailure(category)
	}
	value := string(data)
	if pat {
		value = strings.TrimSpace(value)
		if value == "" || strings.ContainsAny(value, " \t\r\n") {
			return "", pat, startupFailure(category)
		}
	}
	return value, pat, nil
}

func (c *Client) JIT(ctx context.Context, name string) (string, error) {
	return c.JITWithWorkFolder(ctx, name, runnerWorkFolder)
}

// JITWithWorkFolder uses a host-selected reserved workspace, never workflow input.
func (c *Client) JITWithWorkFolder(ctx context.Context, name, workFolder string) (string, error) {
	if !workerapi.ValidWorkFolder(workFolder) {
		return "", fmt.Errorf("invalid reserved work folder")
	}
	if workFolder == "" {
		workFolder = runnerWorkFolder
	}
	j, e := c.API.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{Name: name, WorkFolder: workFolder}, c.SetID)
	if e != nil || j == nil || j.EncodedJITConfig == "" {
		return "", fmt.Errorf("JIT generation failed")
	}
	return j.EncodedJITConfig, nil
}
func (c *Client) Remove(ctx context.Context, name string) error {
	r, e := c.API.GetRunnerByName(ctx, name)
	if e != nil {
		return fmt.Errorf("runner lookup failed")
	}
	if r == nil {
		return nil
	}
	if c.SetID == 0 || r.RunnerScaleSetID != c.SetID {
		return fmt.Errorf("runner ownership mismatch")
	}
	if e = c.API.RemoveRunner(ctx, int64(r.ID)); e != nil {
		return fmt.Errorf("runner removal failed")
	}
	return nil
}

// Poll acquires available requests, then scales using statistics, never message counts.
// No job ID is sent to a VM: GitHub selects any matching idle runner.
func (c *Client) Poll(ctx context.Context, owner string, max int, desired chan<- int) error {
	session, e := c.API.MessageSessionClient(ctx, c.SetID, owner)
	if e != nil {
		return fmt.Errorf("message session creation failed")
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = session.Close(closeCtx)
	}()
	n := 0
	if s := session.Session().Statistics; s != nil {
		n = s.TotalAssignedJobs
	}
	select {
	case desired <- min(max, maxInt(0, n)):
	case <-ctx.Done():
		return ctx.Err()
	}
	return pollMessages(ctx, session, max, desired)
}

// A separate interface keeps ordering and cancellation testable without GitHub.
type messageSession interface {
	GetMessage(context.Context, int, int) (*scaleset.RunnerScaleSetMessage, error)
	AcquireJobs(context.Context, []int64) ([]int64, error)
	DeleteMessage(context.Context, int) error
}

func pollMessages(ctx context.Context, session messageSession, max int, desired chan<- int) error {
	last := 0
	for {
		m, e := session.GetMessage(ctx, last, max)
		if e != nil {
			return fmt.Errorf("demand poll failed")
		}
		if m == nil {
			continue
		}
		// Acquire every offered request before acknowledging the message. Local
		// capacity limits credentialed runners, not ownership of queued demand;
		// dropping excess IDs here strands them after DeleteMessage.
		var ids []int64
		for _, j := range m.JobAvailableMessages {
			ids = append(ids, j.RunnerRequestID)
		}
		if m.Statistics != nil {
			select {
			case desired <- min(max, maxInt(0, m.Statistics.TotalAssignedJobs)):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		// Statistics already describe owned demand. Let warm provisioning overlap
		// acquisition; never infer desired runners from available-message counts.
		if len(ids) > 0 {
			started := time.Now()
			if _, e = session.AcquireJobs(ctx, ids); e != nil {
				return fmt.Errorf("job acquisition failed")
			}
			slog.Info("Scale-set acquisition completed", "duration_ms", time.Since(started).Milliseconds(), "requests", len(ids))
		}
		if e = session.DeleteMessage(ctx, m.MessageID); e != nil {
			return fmt.Errorf("message acknowledgement failed")
		}
		last = m.MessageID
	}
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func validateScope(scope, configuredHost string) error {
	host := configuredHost
	if host == "" {
		host = "github.com"
	}
	authority, err := url.Parse("https://" + host)
	if err != nil || len(host) > 260 || authority.Host != host || authority.Hostname() == "" || authority.User != nil || authority.Path != "" || authority.RawQuery != "" || authority.Fragment != "" {
		return fmt.Errorf("invalid configured GitHub host")
	}
	u, e := url.Parse(scope)
	if e != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, host) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return fmt.Errorf("use HTTPS on the explicitly configured GitHub host")
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) < 1 || len(p) > 2 || p[0] == "" || p[0] == "enterprises" {
		return fmt.Errorf("use repository or organization scope")
	}

	for _, part := range p {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(part) {
			return fmt.Errorf("invalid GitHub scope")
		}
	}
	return nil
}
