//go:build linux

package worker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func fixture() Request {
	return Request{Identity: Identity{WorkerID: "worker", BrokerID: "broker", Generation: 1}, AssignmentID: "assignment", VMID: "vm", ProfileDigest: strings.Repeat("a", 64), CPUs: 2, MemoryMiB: 4096, DiskGiB: 48}
}
func openTest(t *testing.T, dir string, identity Identity) *Journal {
	t.Helper()
	j, err := Open(dir, identity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}
func reserveSeal(t *testing.T, j *Journal, r Request) {
	t.Helper()
	if _, err := j.Reserve(r); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Seal(r); err != nil {
		t.Fatal(err)
	}
}

func TestLostACKAndRestartNeverRepeatDelivery(t *testing.T) {
	dir := privateDir(t)
	r := fixture()
	j := openTest(t, dir, r.Identity)
	reserveSeal(t, j, r)
	if _, err := j.DeliverIntent(r); err != nil {
		t.Fatal(err)
	}
	// Simulate successful serial dispatch followed by a lost broker ACK and crash.
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = openTest(t, dir, r.Identity)
	got, err := j.Reserve(r)
	if err != nil || got.State != DeliveryIntent {
		t.Fatalf("lost reserve ACK: %v %v", got, err)
	}
	got, err = j.Seal(r)
	if err != nil || got.State != DeliveryIntent {
		t.Fatalf("lost seal ACK: %v %v", got, err)
	}
	if _, err = j.DeliverIntent(r); !errors.Is(err, ErrConsumed) {
		t.Fatalf("replayed delivery: %v", err)
	}
	proof := ExitProof{VMID: r.VMID, QEMUExitConfirmed: true, DiskRemoved: true}
	if _, err = j.Terminal(r, proof); err != nil {
		t.Fatal(err)
	}
	if _, err = j.Terminal(r, proof); err != nil {
		t.Fatalf("terminal lost ACK: %v", err)
	}
	fresh := r
	fresh.AssignmentID = "another"
	if _, err = j.Reserve(fresh); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal VM reused: %v", err)
	}
}
func TestConcurrentDuplicateDeliveryHasOneWinner(t *testing.T) {
	r := fixture()
	j := openTest(t, privateDir(t), r.Identity)
	reserveSeal(t, j, r)
	var success atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < 32; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := j.DeliverIntent(r)
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, ErrConsumed) {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	group.Wait()
	if success.Load() != 1 {
		t.Fatalf("credential write permissions issued %d times", success.Load())
	}
}
func TestGenerationFencesOldRequestsAndRetainsConsumedVM(t *testing.T) {
	r := fixture()
	dir := privateDir(t)
	j := openTest(t, dir, r.Identity)
	reserveSeal(t, j, r)
	j.Close()
	next := r.Identity
	next.Generation = 2
	j = openTest(t, dir, next)
	j.Close()
	if older, err := Open(dir, r.Identity); !errors.Is(err, ErrFenced) {
		if older != nil {
			older.Close()
		}
		t.Fatalf("old generation reopened: %v", err)
	}
	j = openTest(t, dir, next)
	if _, err := j.DeliverIntent(r); !errors.Is(err, ErrFenced) {
		t.Fatalf("old epoch: %v", err)
	}
	claim := r
	claim.Identity = next
	if _, err := j.Reserve(claim); !errors.Is(err, ErrConflict) {
		t.Fatalf("old reservation rebound: %v", err)
	}
	if _, err := j.RecoverTerminal(r.AssignmentID, ExitProof{VMID: r.VMID, QEMUExitConfirmed: true, DiskRemoved: true}); err != nil {
		t.Fatal(err)
	}
	claim.AssignmentID = "new"
	if _, err := j.Reserve(claim); !errors.Is(err, ErrConflict) {
		t.Fatalf("old VM recycled: %v", err)
	}
	records, err := j.Records()
	if err != nil || len(records) != 1 || records[0].State != Terminal {
		t.Fatalf("recovery: %v %v", records, err)
	}
}
func TestPersistenceAmbiguityPoisonsUntilReopen(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-commit", true: "after-commit"}[committed], func(t *testing.T) {
			r := fixture()
			dir := privateDir(t)
			j := openTest(t, dir, r.Identity)
			reserveSeal(t, j, r)
			real := j.persist
			j.persist = func(s snapshot) error {
				if committed {
					if err := real(s); err != nil {
						return err
					}
				}
				return errors.New("injected storage failure")
			}
			if _, err := j.DeliverIntent(r); !errors.Is(err, ErrUncertain) {
				t.Fatalf("failure: %v", err)
			}
			if _, err := j.Reserve(r); !errors.Is(err, ErrUncertain) {
				t.Fatalf("admission after uncertain commit: %v", err)
			}
			j.Close()
			j = openTest(t, dir, r.Identity)
			records, err := j.Records()
			if err != nil {
				t.Fatal(err)
			}
			want := Sealed
			if committed {
				want = DeliveryIntent
			}
			if records[0].State != want {
				t.Fatalf("recovered %s want %s", records[0].State, want)
			}
			if committed {
				if _, err := j.DeliverIntent(r); !errors.Is(err, ErrConsumed) {
					t.Fatal("ambiguous committed delivery replayed")
				}
			}
		})
	}
}
func TestTransitionsRequireSealingAndConfirmedCleanup(t *testing.T) {
	r := fixture()
	j := openTest(t, privateDir(t), r.Identity)
	if _, err := j.Seal(r); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := j.Reserve(r); err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeliverIntent(r); !errors.Is(err, ErrTransition) {
		t.Fatalf("unsealed delivery: %v", err)
	}
	for _, proof := range []ExitProof{{VMID: r.VMID}, {VMID: r.VMID, QEMUExitConfirmed: true}, {VMID: r.VMID, DiskRemoved: true}, {VMID: "wrong", QEMUExitConfirmed: true, DiskRemoved: true}} {
		if _, err := j.Terminal(r, proof); !errors.Is(err, ErrTransition) {
			t.Fatalf("invalid cleanup proof: %v", err)
		}
	}
	reserveSeal(t, j, r)
	if _, err := j.Terminal(r, ExitProof{VMID: r.VMID, QEMUExitConfirmed: true, DiskRemoved: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Seal(r); !errors.Is(err, ErrTransition) {
		t.Fatalf("sealed dead VM: %v", err)
	}
}
func TestIdentityShapeAndOwnershipFailClosed(t *testing.T) {
	r := fixture()
	dir := privateDir(t)
	j := openTest(t, dir, r.Identity)
	if _, err := Open(dir, r.Identity); err == nil {
		t.Fatal("duplicate owner accepted")
	}
	if _, err := j.Reserve(r); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Request){func(r *Request) { r.CPUs++ }, func(r *Request) { r.ProfileDigest = strings.Repeat("b", 64) }, func(r *Request) { r.VMID = "other" }} {
		other := r
		change(&other)
		if _, err := j.Reserve(other); !errors.Is(err, ErrConflict) {
			t.Fatalf("rebound shape: %v", err)
		}
	}
	other := r
	other.Identity.BrokerID = "other"
	if _, err := j.Seal(other); !errors.Is(err, ErrFenced) {
		t.Fatal(err)
	}
	j.Close()
	if _, err := Open(dir, other.Identity); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong broker opened journal: %v", err)
	}
	for _, change := range []func(*Request){func(r *Request) { r.AssignmentID = "../escape" }, func(r *Request) { r.MemoryMiB = 0 }, func(r *Request) { r.ProfileDigest = "path/to/image" }} {
		bad := r
		change(&bad)
		j = openTest(t, privateDir(t), r.Identity)
		if _, err := j.Reserve(bad); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
}
func TestJournalBoundsAndMalformedInput(t *testing.T) {
	identity := fixture().Identity
	for _, body := range []string{`{"version":1,"unexpected":true}`, strings.Repeat(" ", MaxJournalBytes+1), `{"version":1,"identity":{"worker_id":"worker","broker_id":"broker","generation":1},"records":{}} {}`} {
		dir := privateDir(t)
		if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if j, err := Open(dir, identity); err == nil {
			j.Close()
			t.Fatal("malformed journal accepted")
		}
	}
	j := openTest(t, privateDir(t), identity)
	// Build a full validated map without 1024 slow fsyncs; exercise admission cap.
	for i := 0; i < MaxRecords; i++ {
		r := fixture()
		r.AssignmentID = stringID(i)
		r.VMID = stringID(i)
		j.data.Records[r.AssignmentID] = Record{Request: r, State: Terminal}
	}
	if _, err := j.Reserve(fixture()); err == nil {
		t.Fatal("record budget exceeded")
	}
}
func stringID(i int) string { return fmt.Sprintf("record-%d", i) }
func TestPrivateFilesAndSymlinksRejected(t *testing.T) {
	r := fixture()
	dir := privateDir(t)
	os.Chmod(dir, 0755)
	if j, err := Open(dir, r.Identity); err == nil {
		j.Close()
		t.Fatal("public directory accepted")
	}
	os.Chmod(dir, 0700)
	target := filepath.Join(privateDir(t), "target")
	os.WriteFile(target, []byte("sensitive"), 0600)
	if err := os.Symlink(target, filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	if j, err := Open(dir, r.Identity); err == nil {
		j.Close()
		t.Fatal("state symlink accepted")
	}
	body, _ := os.ReadFile(target)
	if string(body) != "sensitive" {
		t.Fatal("symlink target modified")
	}
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCompletionTimestampIsDurableAndIdempotent(t *testing.T) {
	r := fixture()
	dir := privateDir(t)
	j := openTest(t, dir, r.Identity)
	reserveSeal(t, j, r)
	proof := ExitProof{VMID: r.VMID, QEMUExitConfirmed: true, DiskRemoved: true}
	record, err := j.Terminal(r, proof)
	if err != nil || record.CompletedAt.IsZero() {
		t.Fatalf("missing cleanup timestamp: %v %v", record, err)
	}
	again, err := j.Terminal(r, proof)
	if err != nil || !again.CompletedAt.Equal(record.CompletedAt) {
		t.Fatal("terminal retry moved completion time")
	}
	j.Close()
	j = openTest(t, dir, r.Identity)
	records, err := j.Records()
	if err != nil || !records[0].CompletedAt.Equal(record.CompletedAt) {
		t.Fatal("completion time not durable")
	}
}
