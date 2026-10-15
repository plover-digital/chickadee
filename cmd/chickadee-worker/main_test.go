//go:build linux && amd64

package main

import (
	"context"
	"encoding/json"
	"github.com/plover-digital/chickadee/internal/github"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/landlockrunner"
	"github.com/plover-digital/chickadee/internal/worker"
	"github.com/plover-digital/chickadee/workerapi"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerConfigRejectsManagementFieldsAndPublicFiles(t *testing.T) {
	c := configuration{Config: worker.Config{Version: 1, Identity: worker.Identity{WorkerID: "worker-one", BrokerID: "broker-one", Generation: 1}, StateDir: "/var/lib/ck-worker", Profiles: []worker.Profile{{ID: "small", Digest: strings.Repeat("a", 64), ImageDir: "/var/lib/chickadee-image/ubuntu", Machine: "q35", CPUs: 2, MemoryMiB: 2048, DiskGiB: 48}}, Budget: worker.Budget{MaxVMs: 1, MaxCPUs: 2, MaxMemoryMiB: 2048}, BootTimeoutSeconds: 120, JobTimeoutSeconds: 3600, ReservationTimeoutSeconds: 180}, Listen: "10.0.0.2:18443"}
	data, _ := json.Marshal(c)
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, data, 0600)
	if _, e := load(p); e != nil {
		t.Fatal(e)
	}
	var fields map[string]any
	json.Unmarshal(data, &fields)
	fields["app_key_file"] = "/etc/chickadee/app.pem"
	bad, _ := json.Marshal(fields)
	os.WriteFile(p, bad, 0600)
	if _, e := load(p); e == nil {
		t.Fatal("GitHub management field accepted by keyless worker")
	}
	os.WriteFile(p, data, 0600)
	os.Chmod(p, 0644)
	if _, e := load(p); e == nil {
		t.Fatal("world readable worker policy accepted")
	}
}

type adapterFixture struct{ reserveCalls int }

func (f *adapterFixture) Inventory() (worker.Inventory, error) {
	return worker.Inventory{Identity: worker.Identity{WorkerID: "worker-one", BrokerID: "broker-one", Generation: 1}, Profiles: []worker.ProfileInventory{{ID: "small", Digest: strings.Repeat("a", 64), CPUs: 2, MemoryMiB: 2048, DiskGiB: 48}}}, nil
}
func (f *adapterFixture) Reserve(ctx context.Context, i worker.Identity, id, profile string) (worker.Record, error) {
	f.reserveCalls++
	return worker.Record{Request: worker.Request{Identity: i, AssignmentID: id, VMID: "0123456789abcdef", ProfileDigest: strings.Repeat("a", 64), CPUs: 2, MemoryMiB: 2048, DiskGiB: 48}, State: worker.Reserved}, nil
}
func (f *adapterFixture) Seal(worker.Identity, string) (worker.Record, error) {
	return worker.Record{}, nil
}
func (f *adapterFixture) Deliver(worker.Identity, string, string) error { return nil }
func (f *adapterFixture) Status(worker.Identity, string) (worker.Record, error) {
	return worker.Record{}, nil
}
func (f *adapterFixture) Drain(worker.Identity) error { return nil }
func TestAdapterRejectsWrongImageDigestBeforeReservation(t *testing.T) {
	f := &adapterFixture{}
	a := engineAdapter{engine: f}
	i := workerapi.Identity{WorkerID: "worker-one", BrokerID: "broker-one", Generation: 1}
	if _, e := a.Reserve(context.Background(), i, "assignment", "small", strings.Repeat("b", 64)); e != workerapi.ErrConflict || f.reserveCalls != 0 {
		t.Fatal("mismatched content identity reserved a VM", e)
	}
	r, e := a.Reserve(context.Background(), i, "assignment", "small", strings.Repeat("a", 64))
	if e != nil || f.reserveCalls != 1 || r.Request.VMID != "0123456789abcdef" {
		t.Fatal(r, e)
	}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "image_dir") || strings.Contains(string(data), "jit") {
		t.Fatal("local path or credentials reached wire record")
	}
}

func TestAdapterPreservesDefinitiveAbsenceAndActualCompletion(t *testing.T) {
	if translate(worker.ErrNotFound) != workerapi.ErrNotFound {
		t.Fatal("missing assignment treated as ambiguous failure")
	}
	completed := time.Now().UTC().Add(-time.Hour)
	out := wireRecord(worker.Record{State: worker.Terminal, CompletedAt: completed})
	if !out.CompletedAt.Equal(completed) {
		t.Fatal("worker cleanup time replaced by later broker observation")
	}
}

func TestCPUPreflightModeReportsEnforcementBoundary(t *testing.T) {
	if got := cpuModeSummary(true, []int{4, 5}); got != "cpu=quota" {
		t.Fatal(got)
	}
	if got := cpuModeSummary(false, []int{4, 5, 6, 7}); got != "cpu=affinity-only(4,5,6,7)" {
		t.Fatal(got)
	}
}

func TestStandaloneConfigProtectsAppKeyAndRequiresExplicitMode(t *testing.T) {
	root, err := os.MkdirTemp("/var/tmp", "chickadee-standalone-config-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	work := filepath.Join(root, "work")
	tools := filepath.Join(root, "tools")
	os.Mkdir(work, 0700)
	os.Mkdir(tools, 0700)
	key := filepath.Join(tools, "app.pem")
	if err := os.WriteFile(key, []byte("nonsecret fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	c := configuration{Config: worker.Config{Version: 1, Identity: worker.Identity{WorkerID: "local", BrokerID: "standalone", Generation: 1}, StateDir: filepath.Join(root, "state"), Cgroup: &host.CgroupConfig{PidsMax: 128}, Profiles: []worker.Profile{{ID: "sandbox", Digest: strings.Repeat("a", 64), ImageDir: filepath.Join(root, "image"), Machine: "landlock", CPUs: 1, MemoryMiB: 512, DiskGiB: 8, HostAccess: &landlockrunner.HostAccess{WorkDir: work}}}, Budget: worker.Budget{MaxVMs: 1, MaxCPUs: 1, MaxMemoryMiB: 512}, BootTimeoutSeconds: 60, JobTimeoutSeconds: 600, ReservationTimeoutSeconds: 120}, GitHub: &github.StandaloneConfig{GitHubURL: "https://github.com/example", ClientID: "fixture", InstallationID: 1, KeyFile: key, ScaleSet: "sandbox", RunnerGroupID: 1}}
	p := filepath.Join(root, "config.json")
	write := func() {
		t.Helper()
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if _, err := load(p); err != nil {
		t.Fatal("standalone config without TLS rejected", err)
	}
	if err := run(p, true); err == nil || !strings.Contains(err.Error(), "explicit -github") {
		t.Fatal("credential config exposed worker API", err)
	}
	c.Profiles[0].HostAccess.ROPaths = []string{tools}
	write()
	if _, err := load(p); err == nil {
		t.Fatal("App key parent exposed to sandbox")
	}
}
