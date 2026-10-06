//go:build linux && amd64

package main

import (
	"context"
	"encoding/json"
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
