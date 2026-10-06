//go:build linux && amd64

package pool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
)

func reloadFixture(t *testing.T) (config.Config, map[string]Backend, chan bootedProfile, chan string, starter) {
	c, _, boots, running, start := profileFixture(t)
	dir, err := os.MkdirTemp("/tmp", "ck-reload-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	c.StateDir = dir
	c.KeyFile = "/private/app.pem"
	c.ClientID = "test-app"
	c.BootSeconds = 120
	for name, image := range c.Images {
		image.OS, image.Version, image.Machine = "ubuntu", "24.04", "microvm"
		c.Images[name] = image
	}
	profile := c.Profiles["chickadee"]
	c.Profiles = nil
	c.Scopes = map[string]config.Scope{"primary": {GitHubURL: "https://github.com/primary", InstallationID: 11, RunnerGroupID: 2, Profiles: map[string]config.Profile{"chickadee": profile}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	owner, err := Acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	jit := make(chan string, 20)
	backends := map[string]Backend{c.ProfileConfigs()[0].Key(): &profileBackend{profile: "chickadee", jit: jit}}
	return c, backends, boots, running, start
}

func copyReloadConfig(t *testing.T, c config.Config) config.Config {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var copy config.Config
	if err = json.Unmarshal(data, &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}

func applyReload(t *testing.T, updates chan<- ProfileUpdate, c config.Config, backends map[string]Backend) {
	t.Helper()
	applied := make(chan error, 1)
	updates <- ProfileUpdate{Config: c, Backends: backends, Applied: applied}
	select {
	case err := <-applied:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("update was not acknowledged")
	}
}

func TestReloadAddsQueueWithoutInterruptingSpentGuest(t *testing.T) {
	c, backends, boots, running, start := reloadFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demand := make(chan Demand, 8)
	updates := make(chan ProfileUpdate)
	done := make(chan error, 1)
	go func() { done <- runProfilesWithUpdates(ctx, c, backends, demand, start, nil, updates) }()
	primary := c.ProfileConfigs()[0].Key()
	demand <- Demand{primary, 1}
	first := awaitBoot(t, boots)
	awaitRunning(t, running)
	next := copyReloadConfig(t, c)
	next.Scopes["beta"] = config.Scope{GitHubURL: "https://github.com/beta/repo", InstallationID: 22, RunnerGroupID: 1, Profiles: map[string]config.Profile{"chickadee-small-ubuntu-2404": {Image: "ubuntu", Resources: "small", Max: 1}}}
	beta := config.ScopeKey("https://github.com/beta/repo", "chickadee-small-ubuntu-2404")
	jit := make(chan string, 2)
	applyReload(t, updates, next, map[string]Backend{beta: &profileBackend{profile: "chickadee-small-ubuntu-2404", jit: jit}})
	select {
	case <-first.m.done:
		t.Fatal("existing job interrupted by admission")
	default:
	}
	demand <- Demand{beta, 1}
	second := awaitBoot(t, boots)
	awaitRunning(t, running)
	if first.id == second.id {
		t.Fatal("credentialed VM was reused")
	}
	select {
	case <-jit:
	case <-time.After(time.Second):
		t.Fatal("new scope did not receive its JIT")
	}
	select {
	case <-first.m.done:
		t.Fatal("admission destroyed another scope job")
	default:
	}
	close(first.m.job)
	close(second.m.job)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pool did not exit")
	}
}

func TestReloadRemovalRetainsSpentIdentityAndIgnoresStaleDemand(t *testing.T) {
	c, backends, boots, running, start := reloadFixture(t)
	beta := config.Scope{GitHubURL: "https://github.com/beta/repo", InstallationID: 22, RunnerGroupID: 1, Profiles: map[string]config.Profile{"chickadee": c.Scopes["primary"].Profiles["chickadee"]}}
	c.Scopes["beta"] = beta
	key := config.ScopeKey(beta.GitHubURL, "chickadee")
	backends[key] = &profileBackend{profile: "chickadee", jit: make(chan string, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demand := make(chan Demand, 8)
	updates := make(chan ProfileUpdate)
	done := make(chan error, 1)
	go func() { done <- runProfilesWithUpdates(ctx, c, backends, demand, start, nil, updates) }()
	demand <- Demand{key, 1}
	spent := awaitBoot(t, boots)
	awaitRunning(t, running)
	next := copyReloadConfig(t, c)
	delete(next.Scopes, "beta")
	applyReload(t, updates, next, nil)
	demand <- Demand{key, 1}
	// Force another actor round and verify its status publication, avoiding
	// timing-dependent assumptions about when a stale message was consumed.
	applyReload(t, updates, next, nil)
	select {
	case <-spent.m.done:
		t.Fatal("removed scope's current job was killed")
	default:
	}
	select {
	case <-boots:
		t.Fatal("removed scope booted from stale demand")
	default:
	}
	close(spent.m.job)
	select {
	case <-spent.m.done:
	case <-time.After(5 * time.Second):
		t.Fatal("spent guest did not retire normally")
	}
	records, err := host.Records(c.StateDir)
	if err != nil || len(records) != 1 || records[0].InstallationID != 22 {
		t.Fatal("removed scope durable intent lost", err)
	}
	// The historical config/backend must remain usable through the record grace.
	changed := copyReloadConfig(t, next)
	beta.InstallationID = 33
	changed.Scopes["beta"] = beta
	ack := make(chan error, 1)
	updates <- ProfileUpdate{Config: changed, Backends: backends, Applied: ack}
	if err := <-ack; err == nil {
		t.Fatal("historical installation overwritten before reconciliation")
	}
	cancel()
	<-done
}

func TestReloadNewScopeBorrowsExistingCredentialFreeWarmGuest(t *testing.T) {
	c, backends, boots, running, start := reloadFixture(t)
	c.Limits = config.Limits{Max: 1, CPUs: 4, MemoryMiB: 8192}
	primary := c.Scopes["primary"]
	p := primary.Profiles["chickadee"]
	p.Warm = 1
	primary.Profiles["chickadee"] = p
	c.Scopes["primary"] = primary
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demand := make(chan Demand, 4)
	updates := make(chan ProfileUpdate)
	done := make(chan error, 1)
	go func() { done <- runProfilesWithUpdates(ctx, c, backends, demand, start, nil, updates) }()
	warm := awaitBoot(t, boots)
	next := copyReloadConfig(t, c)
	p.Warm = 0
	next.Scopes["beta"] = config.Scope{GitHubURL: "https://github.com/beta/repo", InstallationID: 22, RunnerGroupID: 1, Profiles: map[string]config.Profile{"chickadee": p}}
	key := config.ScopeKey("https://github.com/beta/repo", "chickadee")
	jit := make(chan string, 2)
	applyReload(t, updates, next, map[string]Backend{key: &profileBackend{profile: "chickadee", jit: jit}})
	demand <- Demand{key, 1}
	awaitRunning(t, running)
	select {
	case name := <-jit:
		if name != "chickadee-"+warm.id {
			t.Fatal("new tenant did not receive existing warm guest")
		}
	case <-time.After(time.Second):
		t.Fatal("new scope JIT missing")
	}
	select {
	case <-boots:
		t.Fatal("admission rebooted compatible shared warm capacity")
	default:
	}
	records, err := host.Records(c.StateDir)
	if err != nil || len(records) != 1 || records[0].GitHubURL != "https://github.com/beta/repo" {
		t.Fatal("intent bound to old boot scope", err)
	}
	close(warm.m.job)
	cancel()
	<-done
}

func TestValidateReloadFreezesPhysicalHostAndExistingIdentity(t *testing.T) {
	c, _, _, _, _ := reloadFixture(t)
	mutations := []func(*config.Config){
		func(n *config.Config) { n.Limits.MemoryMiB++ },
		func(n *config.Config) { n.KeyFile = "/other/app.pem" },
		func(n *config.Config) {
			image := n.Images["rocky"]
			image.Path = "/other/image"
			n.Images["rocky"] = image
		},
		func(n *config.Config) {
			scope := n.Scopes["primary"]
			scope.InstallationID++
			n.Scopes["primary"] = scope
		},
		func(n *config.Config) {
			scope := n.Scopes["primary"]
			profile := scope.Profiles["chickadee"]
			profile.Resources = "small"
			scope.Profiles["chickadee"] = profile
			n.Scopes["primary"] = scope
		},
	}
	for i, mutate := range mutations {
		next := copyReloadConfig(t, c)
		mutate(&next)
		if err := ValidateReload(c, next); err == nil {
			t.Fatalf("unsafe mutation %d accepted", i)
		}
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "status.json")); !os.IsNotExist(err) {
		t.Fatal("validation performed runtime writes")
	}
}
