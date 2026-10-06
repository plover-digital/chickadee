package github

import (
	"context"
	"fmt"
	"github.com/actions/scaleset"
	"github.com/plover-digital/chickadee/internal/config"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"
)

type Client struct {
	API   *scaleset.Client
	SetID int
}

func New(ctx context.Context, c config.Config) (*Client, error) { return newClient(ctx, c, true) }

// Existing never recreates a removed scale set during journal recovery.
func Existing(ctx context.Context, c config.Config) (*Client, error) { return newClient(ctx, c, false) }
func newClient(ctx context.Context, c config.Config, create bool) (*Client, error) {
	u, e := url.Parse(c.GitHubURL)
	if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("prototype supports https://github.com org/repo scopes")
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) < 1 || len(p) > 2 || p[0] == "" || p[0] == "enterprises" {
		return nil, fmt.Errorf("use repository or organization scope")
	}
	info, e := os.Stat(c.KeyFile)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("App key must be a regular file with mode 0600")
	}
	key, e := os.ReadFile(c.KeyFile)
	if e != nil {
		return nil, e
	}
	// Do not replay non-idempotent JIT creation after an ambiguous HTTP failure.
	api, e := scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{GitHubConfigURL: c.GitHubURL, GitHubAppAuth: scaleset.GitHubAppAuth{ClientID: c.ClientID, InstallationID: c.InstallationID, PrivateKey: string(key)}, SystemInfo: scaleset.SystemInfo{System: "chickadee", Version: "prototype"}}, scaleset.WithTimeout(65*time.Second), scaleset.WithRetryMax(0))
	if e != nil {
		return nil, fmt.Errorf("App client initialization failed")
	}
	set, e := api.GetRunnerScaleSet(ctx, c.RunnerGroupID, c.ScaleSet)
	if e != nil {
		return nil, fmt.Errorf("scale set lookup failed")
	}
	if set == nil && !create {
		return &Client{API: api}, nil
	}
	if set == nil {
		set, e = api.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{Name: c.ScaleSet, RunnerGroupID: c.RunnerGroupID, RunnerSetting: scaleset.RunnerSetting{DisableUpdate: true}})
		if e != nil {
			return nil, fmt.Errorf("scale set creation failed")
		}
	}
	if !set.RunnerSetting.DisableUpdate {
		return nil, fmt.Errorf("existing scale set must disable runner auto-update")
	}
	return &Client{API: api, SetID: set.ID}, nil
}
func (c *Client) JIT(ctx context.Context, name string) (string, error) {
	j, e := c.API.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{Name: name, WorkFolder: "_work"}, c.SetID)
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
		capacity := max
		if m.Statistics != nil {
			capacity = max - m.Statistics.TotalAssignedJobs
		}
		var ids []int64
		for _, j := range m.JobAvailableMessages {
			if len(ids) >= capacity {
				break
			}
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
			slog.Info("Scale-set acquisition completed", "duration_ms", time.Since(started).Milliseconds())
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
