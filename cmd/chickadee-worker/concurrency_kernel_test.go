//go:build linux && amd64

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/landlockrunner"
	"github.com/plover-digital/chickadee/internal/protocol"
	"github.com/plover-digital/chickadee/internal/worker"
	"github.com/plover-digital/chickadee/workerapi"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "-landlock-child" {
		probe := len(os.Args) > 2 && os.Args[2] == "-landlock-probe"
		if landlockrunner.Child(probe) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// This opt-in test runs real sandboxes through the real authenticated API. The
// noncredential job fixture remains live beyond the reported 40-second window.
func TestRealConcurrentHostAccessViaWorkerAPIKeepsLiveWorkspaces(t *testing.T) {
	if os.Getenv("CHICKADEE_HOST_ACCESS_SMOKE") != "1" {
		t.Skip("requires isolated delegated unit, 5GiB memory limit and three allowed CPUs")
	}
	if os.Geteuid() == 0 {
		t.Fatal("run unprivileged")
	}
	var allowed unix.CPUSet
	if err := unix.SchedGetaffinity(0, &allowed); err != nil {
		t.Fatal(err)
	}
	var cpus []int
	for id := 0; id < 1024 && len(cpus) < 3; id++ {
		if allowed.IsSet(id) {
			cpus = append(cpus, id)
		}
	}
	if len(cpus) != 3 {
		t.Fatal("three allowed CPUs required")
	}
	root, err := os.MkdirTemp("", "chickadee-api-concurrency-")
	if err != nil {
		t.Fatal(err)
	}
	parent := os.Getenv("CHICKADEE_TEST_WORKSPACE_ROOT")
	if parent == "" {
		parent = "/var/tmp"
	}
	work, err := os.MkdirTemp(parent, "chickadee-live-work-")
	if err != nil {
		t.Fatal(err)
	}
	// No automatic TempDir deletion: failed exit proof must retain writable state.
	clean := false
	defer func() {
		if clean {
			os.RemoveAll(root)
			os.RemoveAll(work)
		} else {
			t.Log("Retained private fixture state for inspection", root, work)
		}
	}()
	for slot := 1; slot <= 3; slot++ {
		if err := os.Mkdir(filepath.Join(work, fmt.Sprint(slot)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	bundle := filepath.Join(root, "bundle")
	os.Mkdir(bundle, 0700)
	script := []byte("#!/bin/sh\nset -eu\nwork=$(printf '%s' \"$ACTIONS_RUNNER_INPUT_JITCONFIG\" | base64 -d)\nprintf alive > \"$work/sentinel\"\nmkdir \"$work/checkout\"\nprintf checkout > \"$work/checkout/keep\"\nfor i in $(seq 1 160); do test -f \"$work/sentinel\"; test -f \"$work/checkout/keep\"; sleep 0.5; done\nmkdir -p /job/runner/_diag\nprintf CHICKADEE_CONCURRENCY_FIXTURE_COMPLETED > /job/runner/_diag/completed.log\n")
	file, err := os.Create(filepath.Join(bundle, "runner.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	archive := tar.NewWriter(gzipWriter)
	if err := archive.WriteHeader(&tar.Header{Name: "run.sh", Mode: 0700, Size: int64(len(script))}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write(script); err != nil {
		t.Fatal(err)
	}
	archive.Close()
	gzipWriter.Close()
	file.Close()
	os.WriteFile(filepath.Join(bundle, "manifest.json"), []byte(`{"machine":"landlock","disk_gib":8}`), 0600)
	sums := ""
	for _, name := range []string{"runner.tar.gz", "manifest.json"} {
		data, err := os.ReadFile(filepath.Join(bundle, name))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		sums += fmt.Sprintf("%x  %s\n", hash, name)
	}
	os.WriteFile(filepath.Join(bundle, "SHA256SUMS"), []byte(sums), 0600)
	digest := sha256.Sum256([]byte(sums))
	identity := worker.Identity{WorkerID: "kernel-api-probe", BrokerID: "kernel-api-probe", Generation: 1}
	serverTLS, clientTLS := concurrencyCertificates(t, root, identity)
	cfg := worker.Config{Version: 1, Identity: identity, StateDir: filepath.Join(root, "state"), Cgroup: &host.CgroupConfig{PidsMax: 128}, CPUIDs: cpus, Profiles: []worker.Profile{{ID: "sandbox", Digest: hex.EncodeToString(digest[:]), ImageDir: bundle, Machine: "landlock", CPUs: 1, MemoryMiB: 512, DiskGiB: 8, Warm: 3, HostAccess: &landlockrunner.HostAccess{WorkDir: filepath.Join(work, "{slot}")}}}, Budget: worker.Budget{MaxVMs: 3, MaxCPUs: 3, MaxMemoryMiB: 1536}, BootTimeoutSeconds: 30, JobTimeoutSeconds: 120, ReservationTimeoutSeconds: 40, LogRetentionMiB: 64, ProtectedPaths: []string{serverTLS.KeyFile, serverTLS.CertificateFile, serverTLS.CAFile, clientTLS.KeyFile}}
	engine, err := worker.OpenEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	serving := false
	defer func() {
		cancel()
		if !serving {
			engine.Drain(cfg.Identity)
			wait, stop := context.WithTimeout(context.Background(), 330*time.Second)
			defer stop()
			if err := engine.Wait(wait); err == nil {
				clean = !t.Failed()
			} else {
				t.Errorf("cleanup failed; retained fixtures: %v", err)
			}
			return
		}
		select {
		case err := <-done:
			if err == nil {
				clean = !t.Failed()
			} else {
				t.Errorf("cleanup failed; retained fixtures: %v", err)
			}
		case <-time.After(330 * time.Second):
			t.Error("cleanup proof unavailable; retained fixtures")
		}
	}()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := workerapi.NewServer(workerapi.ServerConfig{Listen: listener.Addr().String(), Identity: wireIdentity(identity), TLS: serverTLS}, &engineAdapter{engine: engine})
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	serving = true
	go func() {
		done <- serveWorker(ctx, cfg, engine, srv, func() error { return srv.ServeTLS(listener, "", "") })
	}()
	client, err := workerapi.NewClient(workerapi.ClientConfig{Endpoint: "https://" + listener.Addr().String(), Identity: wireIdentity(identity), ServerName: "worker.test", TLS: clientTLS})
	if err != nil {
		t.Fatal(err)
	}
	calls, stop := context.WithTimeout(context.Background(), 150*time.Second)
	defer stop()
	deadline := time.Now().Add(30 * time.Second)
	for {
		inventory, err := client.Inventory(calls)
		if err == nil && inventory.Profiles[0].Ready == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("warm capacity unavailable", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var records []workerapi.Record
	for slot := 1; slot <= 3; slot++ {
		id := fmt.Sprintf("live-%d", slot)
		record, err := client.Reserve(calls, id, "sandbox", hex.EncodeToString(digest[:]))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Seal(calls, id); err != nil {
			t.Fatal(err)
		}
		if err := client.Deliver(calls, id, base64.StdEncoding.EncodeToString([]byte(record.Request.WorkFolder))); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		ready := true
		for _, record := range records {
			if _, err := os.Stat(filepath.Join(record.Request.WorkFolder, "sentinel")); err != nil {
				ready = false
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture workloads not running")
		}
		time.Sleep(50 * time.Millisecond)
	}
	until := time.Now().Add(50 * time.Second)
	for time.Now().Before(until) {
		inventory, err := client.Inventory(calls)
		if err != nil || inventory.Used.VMs != 3 || inventory.Profiles[0].Booting != 0 || inventory.Profiles[0].Ready != 0 {
			t.Fatal("active capacity changed", inventory, err)
		}
		dirs, err := os.ReadDir(filepath.Join(cfg.StateDir, "vms"))
		if err != nil || len(dirs) != 3 {
			t.Fatal("phantom runtime appeared", dirs, err)
		}
		for _, record := range records {
			status, err := client.Status(calls, record.Request.AssignmentID)
			if err != nil || status.State != "delivery_intent" {
				t.Fatal("live assignment changed", status, err)
			}
			if _, err := os.Stat(filepath.Join(cfg.StateDir, "vms", record.Request.VMID)); err != nil {
				t.Fatal("live runtime removed", err)
			}
			for _, name := range []string{"sentinel", "checkout/keep"} {
				if _, err := os.Stat(filepath.Join(record.Request.WorkFolder, name)); err != nil {
					t.Fatal("live workspace cleared", err)
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := client.Drain(calls); err != nil {
		t.Fatal(err)
	}
	for {
		terminal := true
		for _, record := range records {
			status, err := client.Status(calls, record.Request.AssignmentID)
			if err != nil {
				t.Fatal(err)
			}
			terminal = terminal && status.State == "terminal"
		}
		if terminal {
			break
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-calls.Done():
			t.Fatal("terminal cleanup timeout")
		}
	}
	for _, record := range records {
		// Terminal state alone does not prove the fixture exited successfully.
		// Require diagnostics emitted only after all 80 seconds of sentinel checks,
		// including the period after draining begins.
		file, err := os.Open(filepath.Join(cfg.StateDir, "logs", record.Request.VMID+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		reader := json.NewDecoder(file)
		var diagnostics []byte
		for {
			var frame protocol.Frame
			err := reader.Decode(&frame)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					file.Close()
					t.Fatal(err)
				}
				break
			}
			data, err := base64.StdEncoding.DecodeString(frame.Data)
			if err != nil {
				file.Close()
				t.Fatal(err)
			}
			diagnostics = append(diagnostics, data...)
		}
		file.Close()
		if !bytes.Contains(diagnostics, []byte("CHICKADEE_CONCURRENCY_FIXTURE_COMPLETED")) {
			t.Fatal("fixture did not complete all live-workspace checks", record.Request.AssignmentID)
		}
		entries, err := os.ReadDir(record.Request.WorkFolder)
		if err != nil || len(entries) != 0 {
			t.Fatal("terminal workspace retained job data", entries, err)
		}
	}
	t.Log("Three real API-driven workloads stayed intact past40s; cleaned only after completion")
}

func concurrencyCertificates(t *testing.T, root string, identity worker.Identity) (workerapi.TLSFiles, workerapi.TLSFiles) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture-ca"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(root, "ca.crt")
	os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	issue := func(role, id string) workerapi.TLSFiles {
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		uri, _ := url.Parse("spiffe://chickadee/" + role + "/" + id)
		serial := int64(2)
		if role == "broker" {
			serial = 3
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), DNSNames: []string{"worker.test"}, URIs: []*url.URL{uri}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		keyDer, err := x509.MarshalPKCS8PrivateKey(leafKey)
		if err != nil {
			t.Fatal(err)
		}
		files := workerapi.TLSFiles{CertificateFile: filepath.Join(root, role+".crt"), KeyFile: filepath.Join(root, role+".key"), CAFile: caFile}
		if err := os.WriteFile(files.CertificateFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDer}), 0600); err != nil {
			t.Fatal(err)
		}
		return files
	}
	return issue("worker", identity.WorkerID), issue("broker", identity.BrokerID)
}
