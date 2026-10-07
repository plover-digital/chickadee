package workerapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type testBackend struct {
	mu           sync.Mutex
	identity     Identity
	record       Record
	delivered    int
	calls        int
	reflectError bool
}

func (b *testBackend) Inventory() (Inventory, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	return Inventory{Identity: b.identity, Budget: Budget{2, 4, 4096}, Profiles: []ProfileInventory{{ID: "small", Digest: strings.Repeat("a", 64), Machine: "q35", CPUs: 2, MemoryMiB: 2048, DiskGiB: 48, Ready: 1}}}, nil
}
func (b *testBackend) Reserve(_ context.Context, i Identity, id, profile, digest string) (Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.record.Request.AssignmentID != "" {
		if b.record.Request.AssignmentID != id {
			return Record{}, ErrConflict
		}
		return b.record, nil
	}
	b.record = Record{Request: Request{i, id, "0123456789abcdef", digest, 2, 2048, 48}, State: "reserved"}
	return b.record, nil
}
func (b *testBackend) Seal(i Identity, id string) (Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.record.State != "reserved" && b.record.State != "sealed" {
		return Record{}, ErrConflict
	}
	b.record.State = "sealed"
	return b.record, nil
}
func (b *testBackend) Deliver(i Identity, id, jit string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.reflectError {
		return fmt.Errorf("sensitive payload: %s", jit)
	}
	if b.record.State == "delivery_intent" {
		return ErrConsumed
	}
	if b.record.State != "sealed" {
		return ErrConflict
	}
	b.record.State = "delivery_intent"
	b.delivered++
	return nil
}
func (b *testBackend) Status(i Identity, id string) (Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.record.Request.AssignmentID != id {
		return Record{}, ErrNotFound
	}
	return b.record, nil
}
func (b *testBackend) Drain(i Identity) error { return nil }

type fixture struct {
	server                *httptest.Server
	backend               *testBackend
	client                *Client
	worker, broker, other TLSFiles
	identity              Identity
}

func newFixture(t *testing.T) *fixture { return newFixtureHandler(t, nil) }
func newFixtureHandler(t *testing.T, wrap func(Identity, *testBackend) http.Handler) *fixture {
	t.Helper()
	dir := t.TempDir()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.pem")
	os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600)
	mint := func(name, uri string, serial int64) TLSFiles {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		u, _ := url.Parse(uri)
		cert := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, URIs: []*url.URL{u}, DNSNames: []string{"worker.test"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, e := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
		if e != nil {
			t.Fatal(e)
		}
		keyDER, _ := x509.MarshalECPrivateKey(key)
		files := TLSFiles{filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key"), caPath}
		os.WriteFile(files.CertificateFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
		os.WriteFile(files.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600)
		return files
	}
	i := Identity{"worker-one", "broker-one", 1}
	f := &fixture{identity: i, worker: mint("worker", workerURI(i.WorkerID), 2), broker: mint("broker", brokerURI(i.BrokerID), 3), other: mint("other", brokerURI("wrong-broker"), 4)}
	f.backend = &testBackend{identity: i}
	handler := NewHandler(i, f.backend)
	if wrap != nil {
		handler = wrap(i, f.backend)
	}
	s := httptest.NewUnstartedServer(handler)
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.TLS, err = serverTLS(f.worker)
	if err != nil {
		t.Fatal(err)
	}
	s.StartTLS()
	t.Cleanup(s.Close)
	f.server = s
	f.client, err = NewClient(ClientConfig{s.URL, i, "worker.test", f.broker})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *fixture) raw(t *testing.T, files TLSFiles, body string, path string) (int, string) {
	t.Helper()
	tc, e := clientTLS(files, "worker.test", f.identity.WorkerID)
	if e != nil {
		t.Fatal(e)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tc}}
	defer client.CloseIdleConnections()
	resp, e := client.Post(f.server.URL+path, "application/json", strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}
func TestMTLSLifecycleAndConsumedReplay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v, e := f.client.Inventory(ctx)
	if e != nil || v.Identity != f.identity {
		t.Fatal(v, e)
	}
	r, e := f.client.Reserve(ctx, "assignment", "small", strings.Repeat("a", 64))
	if e != nil || r.Request.VMID == "" {
		t.Fatal(r, e)
	}
	if _, e = f.client.Seal(ctx, "assignment"); e != nil {
		t.Fatal(e)
	}
	if e = f.client.Deliver(ctx, "assignment", "dGVzdA=="); e != nil {
		t.Fatal(e)
	}
	if e = f.client.Deliver(ctx, "assignment", "dGVzdA=="); !errors.Is(e, ErrConsumed) {
		t.Fatal("replayed delivery accepted", e)
	}
	if f.backend.delivered != 1 {
		t.Fatal("multiple credential deliveries")
	}
	r, e = f.client.Status(ctx, "assignment")
	if e != nil || r.State != "delivery_intent" {
		t.Fatal(r, e)
	}
}
func TestAuthenticatedWrongBrokerCannotReachEngine(t *testing.T) {
	f := newFixture(t)
	code, _ := f.raw(t, f.other, `{"version":1,"identity":{"worker_id":"worker-one","broker_id":"broker-one","generation":1}}`, "/v1/inventory")
	if code != 403 || f.backend.calls != 0 {
		t.Fatal(code, f.backend.calls)
	}
}
func TestStaleGenerationAndStrictJSON(t *testing.T) {
	f := newFixture(t)
	old := f.identity
	old.Generation = 2
	c, e := NewClient(ClientConfig{f.server.URL, old, "worker.test", f.broker})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Inventory(context.Background()); !errors.Is(e, ErrFenced) {
		t.Fatal("generation not fenced", e)
	}
	for _, body := range []string{`{"version":1,"identity":{"worker_id":"worker-one","broker_id":"broker-one","generation":1},"terminal":true}`, `{"version":1,"identity":{"worker_id":"worker-one","broker_id":"broker-one","generation":1}} {}`, `{"version":1,"version":1,"identity":{"worker_id":"worker-one","broker_id":"broker-one","generation":1}}`,
		`{"version":99,"identity":{"worker_id":"worker-one","broker_id":"broker-one","generation":1}}`} {
		code, _ := f.raw(t, f.broker, body, "/v1/inventory")
		if code != 400 {
			t.Fatal(code)
		}
	}
	if f.backend.calls != 0 {
		t.Fatal("invalid request reached engine")
	}
}
func TestOversizeAndCredentialErrorsStayBounded(t *testing.T) {
	f := newFixture(t)
	body := `{"version":1,"identity":{"worker_id":"worker-one","broker_id":"broker-one","generation":1},"assignment_id":"assignment","jit":"` + strings.Repeat("A", MaxRequestBytes) + `"}`
	code, data := f.raw(t, f.broker, body, "/v1/deliver")
	if code == 200 || len(data) > 256 || f.backend.calls != 0 {
		t.Fatal(code, len(data), f.backend.calls)
	}
	f.backend.reflectError = true
	code, data = f.raw(t, f.broker, `{"version":1,"identity":{"worker_id":"worker-one","broker_id":"broker-one","generation":1},"assignment_id":"assignment","jit":"c2Vuc2l0aXZl"}`, "/v1/deliver")
	if code != 503 || strings.Contains(data, "c2Vuc2l0aXZl") {
		t.Fatal("credential reflected", code, data)
	}
}
func TestTLSCertificateAndVersionRequired(t *testing.T) {
	f := newFixture(t)
	cert, pool, e := loadTLS(f.broker)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []*tls.Config{{RootCAs: pool, ServerName: "worker.test", MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13}, {RootCAs: pool, Certificates: []tls.Certificate{cert}, ServerName: "worker.test", MaxVersion: tls.VersionTLS12}} {
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: tc}}
		_, e = c.Post(f.server.URL+"/v1/inventory", "application/json", bytes.NewReader(nil))
		c.CloseIdleConnections()
		if e == nil {
			t.Fatal("unauthenticated or old TLS accepted")
		}
	}
	wrong, e := NewClient(ClientConfig{f.server.URL, Identity{"wrong-worker", "broker-one", 1}, "worker.test", f.broker})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = wrong.Inventory(context.Background()); e == nil {
		t.Fatal("worker URI not pinned")
	}
}
func TestPublicWildcardBindIsRejected(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:18443", "[::]:18443", "8.8.8.8:18443", "example.com:18443"} {
		if privateListen(addr) {
			t.Fatal(addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:18443", "10.0.0.2:18443", "192.168.1.2:18443"} {
		if !privateListen(addr) {
			t.Fatal(addr)
		}
	}
}

func TestMissingAssignmentIsDistinctFromUnavailableAndUnknownRoute(t *testing.T) {
	f := newFixture(t)
	if _, err := f.client.Status(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("absence not definitive", err)
	}
	code, data := f.raw(t, f.broker, `{"version":1,"identity":{"worker_id":"worker-one","broker_id":"broker-one","generation":1}}`, "/v1/terminal")
	if code != 404 || strings.Contains(data, "not_found") {
		t.Fatal("unknown route confused with absent assignment", code, data)
	}
}
