//go:build linux && amd64

package pool

import (
	"context"
	"errors"
	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProfileMachine struct {
	done    chan struct{}
	job     chan struct{}
	once    sync.Once
	dir     string
	running chan string
	profile string
}

func (m *fakeProfileMachine) Run(_ string, _ time.Duration, _ string) error {
	m.running <- m.profile
	select {
	case <-m.job:
		return nil
	case <-m.done:
		return context.Canceled
	}
}
func (m *fakeProfileMachine) Exited() <-chan struct{} { return m.done }
func (m *fakeProfileMachine) Stop() error             { m.once.Do(func() { close(m.done) }); return nil }
func (m *fakeProfileMachine) Cleanup() error          { m.Stop(); return os.RemoveAll(m.dir) }

type profileBackend struct {
	profile string
	jit     chan string
}

func (b *profileBackend) JIT(_ context.Context, name string) (string, error) {
	if !strings.HasPrefix(name, b.profile+"-") {
		return "", errors.New("wrong scale set")
	}
	b.jit <- name
	return "test", nil
}
func (b *profileBackend) Remove(context.Context, string) error { return nil }

type bootedProfile struct {
	c    config.Config
	slot int
	id   string
	m    *fakeProfileMachine
}

func profileFixture(t *testing.T) (config.Config, map[string]Backend, chan bootedProfile, chan string, starter) {
	t.Helper()
	c := config.Config{StateDir: privateTemp(t), Profiles: map[string]config.Profile{"chickadee": {Image: "rocky", Resources: "medium", Max: 1}, "chickadee-small-ubuntu-2404": {Image: "ubuntu", Resources: "small", Max: 2}}, Images: map[string]config.Image{"rocky": {Path: "/images/rocky", DiskGiB: 16}, "ubuntu": {Path: "/images/ubuntu", DiskGiB: 16}}, ResourceClasses: map[string]config.Resources{"small": {CPUs: 2, MemoryMiB: 4096}, "medium": {CPUs: 4, MemoryMiB: 8192}}, Limits: config.Limits{Max: 2, CPUs: 6, MemoryMiB: 12288}, JobSeconds: 60}
	jit := make(chan string, 20)
	backends := map[string]Backend{}
	for name := range c.Profiles {
		backends[name] = &profileBackend{profile: name, jit: jit}
	}
	boots := make(chan bootedProfile, 20)
	running := make(chan string, 20)
	start := func(_ context.Context, c config.Config, slot int, id string) (machine, error) {
		dir := filepath.Join(c.StateDir, "vms", id)
		if e := os.MkdirAll(dir, 0700); e != nil {
			return nil, e
		}
		if e := os.WriteFile(filepath.Join(dir, "disk.qcow2"), nil, 0600); e != nil {
			return nil, e
		}
		m := &fakeProfileMachine{done: make(chan struct{}), job: make(chan struct{}), dir: dir, running: running, profile: c.ScaleSet}
		boots <- bootedProfile{c, slot, id, m}
		return m, nil
	}
	owner, e := Acquire(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { owner.Close() })
	return c, backends, boots, running, start
}
func awaitBoot(t *testing.T, boots <-chan bootedProfile) bootedProfile {
	t.Helper()
	select {
	case b := <-boots:
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("no boot")
		return bootedProfile{}
	}
}
func awaitRunning(t *testing.T, running <-chan string) string {
	t.Helper()
	select {
	case p := <-running:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no job")
		return ""
	}
}
func TestProfilesRouteAndShareBudgets(t *testing.T) {
	c, backends, boots, running, start := profileFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demand := make(chan Demand, 10)
	done := make(chan error, 1)
	go func() { done <- runProfiles(ctx, c, backends, demand, start) }()
	demand <- Demand{"chickadee", 1}
	medium := awaitBoot(t, boots)
	if awaitRunning(t, running) != "chickadee" || medium.c.CPUs != 4 || medium.c.MemoryMiB != 8192 || medium.c.ImageDir != "/images/rocky" {
		t.Fatal("wrong default shape/image")
	}
	demand <- Demand{"chickadee-small-ubuntu-2404", 2}
	small := awaitBoot(t, boots)
	if awaitRunning(t, running) != small.c.ScaleSet || small.c.CPUs != 2 || small.c.MemoryMiB != 4096 || small.slot == medium.slot {
		t.Fatal("cross-profile resource/TAP error")
	}
	select {
	case <-boots:
		t.Fatal("aggregate memory/CPU budget exceeded")
	case <-time.After(1100 * time.Millisecond):
	}
	demand <- Demand{"chickadee", 0}
	close(medium.m.job)
	next := awaitBoot(t, boots)
	if next.c.ScaleSet != small.c.ScaleSet || next.id == medium.id {
		t.Fatal("waiting demand not admitted")
	}
	awaitRunning(t, running)
	if _, e := os.Stat(medium.m.dir); !os.IsNotExist(e) {
		t.Fatal("slot reused before disk cleanup")
	}
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	dirs, _ := os.ReadDir(filepath.Join(c.StateDir, "vms"))
	if len(dirs) != 0 {
		t.Fatal("shutdown leaked disks")
	}
}
func TestProfilesReclaimOnlyUncredentialedWarm(t *testing.T) {
	c, backends, boots, running, start := profileFixture(t)
	c.Limits = config.Limits{Max: 1, CPUs: 4, MemoryMiB: 8192}
	smallName := "chickadee-small-ubuntu-2404"
	p := c.Profiles[smallName]
	p.Max = 1
	p.Warm = 1
	c.Profiles[smallName] = p
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demand := make(chan Demand, 10)
	done := make(chan error, 1)
	go func() { done <- runProfiles(ctx, c, backends, demand, start) }()
	warm := awaitBoot(t, boots)
	demand <- Demand{"chickadee", 1}
	medium := awaitBoot(t, boots)
	if medium.c.ScaleSet != "chickadee" {
		t.Fatal("warm blocked demanded default")
	}
	awaitRunning(t, running)
	select {
	case <-warm.m.done:
	default:
		t.Fatal("reused slot before warm exit")
	}
	demand <- Demand{smallName, 1}
	select {
	case <-medium.m.done:
		t.Fatal("evicted credentialed job")
	case <-boots:
		t.Fatal("booted over max")
	case <-time.After(1100 * time.Millisecond):
	}
	demand <- Demand{"chickadee", 0}
	close(medium.m.job)
	replacement := awaitBoot(t, boots)
	if replacement.c.ScaleSet != smallName {
		t.Fatal("queued request starved")
	}
	awaitRunning(t, running)
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestRemovedProfileRecoveryAndScope(t *testing.T) {
	c, _, _, _, _ := profileFixture(t)
	r := host.Record{ID: "0123456789abcdef", Name: "chickadee-medium-rocky-98-0123456789abcdef"}
	if e := host.Save(c.StateDir, r); e != nil {
		t.Fatal(e)
	}
	b := &removalFailure{ok: true}
	if e := reconcileProfiles(context.Background(), c, map[string]Backend{"chickadee-medium-rocky-98": b}); e != nil {
		t.Fatal(e)
	}
	records, _ := host.Records(c.StateDir)
	if len(records) != 0 {
		t.Fatal("removed profile intent leaked")
	}
	r.GitHubURL = "https://github.com/other"
	if _, e := RecordProfile(c, r); e == nil {
		t.Fatal("changed scope accepted")
	}
}
