//go:build linux && amd64

package main

import (
	"context"
	"github.com/plover-digital/chickadee/internal/worker"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

type drainingJobFixture struct {
	drained, waiting, finished chan struct{}
	once                       sync.Once
}

func (e *drainingJobFixture) Drain(worker.Identity) error {
	e.once.Do(func() { close(e.drained) })
	return nil
}
func (e *drainingJobFixture) Wait(ctx context.Context) error {
	close(e.waiting)
	select {
	case <-e.finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestGracefulShutdownKeepsStatusUntilCredentialedJobFinishes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "delivery_intent")
	})}
	// Authentication is independently covered by workerapi's actual mTLS tests.
	// This seam isolates shutdown ordering from filesystem/KVM prerequisites.
	job := &drainingJobFixture{drained: make(chan struct{}), waiting: make(chan struct{}), finished: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	c := worker.Config{Identity: worker.Identity{WorkerID: "worker-one", BrokerID: "broker-one", Generation: 1}, BootTimeoutSeconds: 1, JobTimeoutSeconds: 1}
	go func() { done <- serveWorker(ctx, c, job, srv, func() error { return srv.Serve(listener) }) }()
	defer srv.Close()
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	status := func() {
		t.Helper()
		resp, err := client.Get("http://" + listener.Addr().String() + "/v1/status")
		if err != nil {
			t.Fatal("status listener unavailable", err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(data) != "delivery_intent" {
			t.Fatal("job status lost", resp.StatusCode, string(data))
		}
	}
	status()
	cancel()
	select {
	case <-job.drained:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not stop admission")
	}
	select {
	case <-job.waiting:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not wait for credentialed job")
	}
	select {
	case err := <-done:
		t.Fatalf("canceled signal context canceled running job wait: %v", err)
	default:
	}
	status() // HTTP reconciliation remains available after the signal context ended.
	close(job.finished)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after job cleanup")
	}
	if _, err = client.Get("http://" + listener.Addr().String() + "/v1/status"); err == nil {
		t.Fatal("listener remained open after engine drain completed")
	}
}
