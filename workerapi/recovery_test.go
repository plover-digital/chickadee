package workerapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type runningTestBackend struct {
	*testBackend
	started, finish, done chan struct{}
}

func (b *runningTestBackend) Deliver(i Identity, id, jit string) error {
	if err := b.testBackend.Deliver(i, id, jit); err != nil {
		return err
	}
	close(b.started)
	go func() {
		<-b.finish
		b.mu.Lock()
		b.record.State = "terminal"
		b.record.CompletedAt = time.Now().UTC()
		b.mu.Unlock()
		close(b.done)
	}()
	return nil
}

type dropAcknowledgement struct {
	http.ResponseWriter
	status int
}

func (w *dropAcknowledgement) WriteHeader(status int) {
	w.status = status
	if status != http.StatusOK {
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *dropAcknowledgement) Write(data []byte) (int, error) {
	if w.status != http.StatusOK {
		return w.ResponseWriter.Write(data)
	}
	conn, _, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err != nil {
		return 0, err
	}
	conn.Close()
	return 0, io.ErrClosedPipe
}
func TestLostDeliveryAcknowledgementPreservesJobAndNeverRetriesJIT(t *testing.T) {
	var running *runningTestBackend
	var dropped sync.Once
	f := newFixtureHandler(t, func(i Identity, b *testBackend) http.Handler {
		running = &runningTestBackend{testBackend: b, started: make(chan struct{}), finish: make(chan struct{}), done: make(chan struct{})}
		handler := NewHandler(i, running)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			drop := false
			if r.URL.Path == "/v1/deliver" {
				dropped.Do(func() { drop = true })
			}
			if drop {
				handler.ServeHTTP(&dropAcknowledgement{ResponseWriter: w}, r)
			} else {
				handler.ServeHTTP(w, r)
			}
		})
	})
	ctx := context.Background()
	if _, err := f.client.Reserve(ctx, "assignment", "small", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.Seal(ctx, "assignment"); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Deliver(ctx, "assignment", "dGVzdA=="); err == nil {
		t.Fatal("lost acknowledgement reported success")
	}
	select {
	case <-running.started:
	case <-time.After(time.Second):
		t.Fatal("delivery was not accepted")
	}
	select {
	case <-running.done:
		t.Fatal("HTTPS disconnect canceled the accepted job")
	default:
	}
	running.mu.Lock()
	count := running.delivered
	calls := running.calls
	running.mu.Unlock()
	// Reserve, Seal and Deliver: POST was not retried after worker acceptance.
	if count != 1 || calls != 3 {
		t.Fatalf("ambiguous delivery replayed: deliveries=%d operations=%d", count, calls)
	}
	status, err := f.client.Status(ctx, "assignment")
	if err != nil || status.State != "delivery_intent" {
		t.Fatal(status, err)
	}
	if err = f.client.Deliver(ctx, "assignment", "dGVzdA=="); !errors.Is(err, ErrConsumed) {
		t.Fatal("explicit replay not fenced", err)
	}
	close(running.finish)
	select {
	case <-running.done:
	case <-time.After(time.Second):
		t.Fatal("accepted job could not finish independently")
	}
	status, err = f.client.Status(ctx, "assignment")
	if err != nil || status.State != "terminal" || status.CompletedAt.IsZero() {
		t.Fatal("completion lost after disconnected delivery", status, err)
	}
	running.mu.Lock()
	defer running.mu.Unlock()
	if running.delivered != 1 {
		t.Fatal("more than one credential delivery")
	}
}

type blockedInventory struct {
	*testBackend
	started, release chan struct{}
}

func (b *blockedInventory) Inventory() (Inventory, error) {
	close(b.started)
	<-b.release
	return b.testBackend.Inventory()
}
func TestBlockedInventoryHonorsCallerDeadlineWithoutClaimingAbsence(t *testing.T) {
	var blocked *blockedInventory
	f := newFixtureHandler(t, func(i Identity, b *testBackend) http.Handler {
		blocked = &blockedInventory{testBackend: b, started: make(chan struct{}), release: make(chan struct{})}
		return NewHandler(i, blocked)
	})
	defer close(blocked.release)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := f.client.Inventory(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrNotFound) {
		t.Fatal("blocked inventory was not an ambiguous bounded deadline", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("caller deadline ignored")
	}
	select {
	case <-blocked.started:
	default:
		t.Fatal("request did not reach blocked authenticated inventory")
	}
}
func TestUnresponsiveTLSConnectionHonorsCallerDeadline(t *testing.T) {
	f := newFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	gone := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			close(gone)
			return
		}
		defer conn.Close()
		close(accepted)
		_, _ = io.Copy(io.Discard, conn)
		close(gone)
	}()
	client, err := NewClient(ClientConfig{Endpoint: "https://" + listener.Addr().String(), Identity: f.identity, ServerName: "worker.test", TLS: f.broker})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = client.Inventory(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrNotFound) || time.Since(start) > 2*time.Second {
		t.Fatal("unresponsive endpoint escaped caller deadline", err)
	}
	select {
	case <-accepted:
	default:
		t.Fatal("connection did not reach TLS blackhole")
	}
	select {
	case <-gone:
	case <-time.After(time.Second):
		t.Fatal("canceled TLS attempt leaked connection")
	}
}

func TestCanceledInventoryDoesNotInterruptOtherActiveStatus(t *testing.T) {
	inventoryStarted, statusStarted := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	f := newFixtureHandler(t, func(i Identity, b *testBackend) http.Handler {
		b.record = Record{Request: Request{i, "assignment", "0123456789abcdef", strings.Repeat("a", 64), 2, 2048, 48}, State: "delivery_intent"}
		handler := NewHandler(i, b)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/inventory" {
				close(inventoryStarted)
				<-r.Context().Done()
				return
			}
			if r.URL.Path == "/v1/status" {
				close(statusStarted)
				<-release
			}
			handler.ServeHTTP(w, r)
		})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inventoryDone := make(chan error, 1)
	go func() { _, err := f.client.Inventory(ctx); inventoryDone <- err }()
	select {
	case <-inventoryStarted:
	case <-time.After(time.Second):
		t.Fatal("inventory did not start")
	}
	statusDone := make(chan error, 1)
	go func() { _, err := f.client.Status(context.Background(), "assignment"); statusDone <- err }()
	select {
	case <-statusStarted:
	case <-time.After(time.Second):
		t.Fatal("status did not become active")
	}
	cancel()
	select {
	case err := <-inventoryDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("inventory cancellation ignored")
	}
	select {
	case err := <-statusDone:
		t.Fatalf("canceling inventory interrupted active status: %v", err)
	default:
	}
	// Release without closing so the deferred cleanup cannot double-close.
	release <- struct{}{}
	select {
	case err := <-statusDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("active status failed after unrelated cancellation")
	}
}
