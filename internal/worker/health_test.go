package worker

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/plover-digital/chickadee/workerapi"
)

func healthOf(t *testing.T, e *Engine) workerapi.Health {
	t.Helper()
	h, err := e.Health()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHealthOpenReportsHeadroom(t *testing.T) {
	c := engineConfig(t)
	e, _ := testEngine(t, c)
	defer e.journal.Close()
	e.mu.Lock()
	e.diskAvailable = func(Config) (int64, error) { return 500 << 30, nil }
	e.mu.Unlock()
	h := healthOf(t, e)
	if h.Admission != workerapi.AdmissionOpen || len(h.Reasons) != 0 || h.Journal.MaxRecords != MaxRecords || h.Disk == nil || h.Disk.AvailableBytes != 500<<30 || h.Disk.RequiredBytes <= 0 {
		t.Fatalf("%+v", h)
	}
}

func TestHealthReportsLowDiskAsRefusing(t *testing.T) {
	c := engineConfig(t)
	c.Profiles[0].Warm = 0
	e, _ := testEngine(t, c)
	defer e.journal.Close()
	setDisk(e, 1<<30)
	if _, err := e.Reserve(context.Background(), c.Identity, "new", "medium"); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	h := healthOf(t, e)
	if h.Admission != workerapi.AdmissionRefusing || !reflect.DeepEqual(h.Reasons, []string{workerapi.ReasonDiskReserve}) {
		t.Fatalf("%+v", h)
	}
}

func TestHealthReportsUnmeasuredDiskAsUnknown(t *testing.T) {
	c := engineConfig(t)
	e, _ := testEngine(t, c)
	defer e.journal.Close()
	e.mu.Lock()
	e.diskAvailable = func(Config) (int64, error) { return 0, errors.New("statfs") }
	e.mu.Unlock()
	if h := healthOf(t, e); h.Disk != nil || h.Admission != workerapi.AdmissionRefusing || !reflect.DeepEqual(h.Reasons, []string{workerapi.ReasonDiskReserve}) {
		t.Fatalf("%+v", h)
	}
}

func TestHealthReportsFullJournal(t *testing.T) {
	c := engineConfig(t)
	e, _ := testEngine(t, c)
	defer e.journal.Close()
	e.mu.Lock()
	e.diskAvailable = func(Config) (int64, error) { return 500 << 30, nil }
	e.mu.Unlock()
	fillJournal(e, c)
	h := healthOf(t, e)
	if h.Admission != workerapi.AdmissionStopped || !reflect.DeepEqual(h.Reasons, []string{workerapi.ReasonJournalFull}) || h.Journal.Records != MaxRecords {
		t.Fatalf("%+v", h)
	}
}

func TestHealthDistinguishesDrainFromFatal(t *testing.T) {
	c := engineConfig(t)
	e, _ := testEngine(t, c)
	e.mu.Lock()
	e.diskAvailable = func(Config) (int64, error) { return 500 << 30, nil }
	e.mu.Unlock()
	if err := e.Drain(c.Identity); err != nil {
		t.Fatal(err)
	}
	if h := healthOf(t, e); h.Admission != workerapi.AdmissionStopped || !reflect.DeepEqual(h.Reasons, []string{workerapi.ReasonDrainRequested}) {
		t.Fatalf("drain: %+v", h)
	}
	e.fail(errors.New("persistence"))
	if h := healthOf(t, e); h.Admission != workerapi.AdmissionStopped || !reflect.DeepEqual(h.Reasons, []string{workerapi.ReasonFatal}) {
		t.Fatalf("fatal: %+v", h)
	}
	e.journal.Close()
}

func TestHealthClearsDiskReserveWhenSpaceReturns(t *testing.T) {
	c := engineConfig(t)
	c.Profiles[0].Warm = 0 // nothing retries this profile on its own
	e, _ := testEngine(t, c)
	defer e.journal.Close()
	setDisk(e, 0)
	if _, err := e.Reserve(context.Background(), c.Identity, "new", "medium"); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if h := healthOf(t, e); h.Admission != workerapi.AdmissionRefusing {
		t.Fatalf("low: %+v", h)
	}
	setDisk(e, 1<<50)
	// Reserve keeps refusing until the backoff ends; health must agree.
	if h := healthOf(t, e); h.Admission != workerapi.AdmissionRefusing {
		t.Fatalf("health open while Reserve still refuses: %+v", h)
	}
	e.mu.Lock()
	e.nextBoot["medium"] = time.Time{}
	e.mu.Unlock()
	if h := healthOf(t, e); h.Admission != workerapi.AdmissionOpen || len(h.Reasons) != 0 {
		t.Fatalf("recovered disk still refusing: %+v", h)
	}
	reserveEngine(t, e, c.Identity, "after", "medium")
}

func TestHealthReportsPersistenceLatch(t *testing.T) {
	c := engineConfig(t)
	e, _ := testEngine(t, c)
	e.journal.mu.Lock()
	e.journal.poisoned = true
	e.journal.mu.Unlock()
	e.fail(ErrUncertain)
	h := healthOf(t, e)
	if h.Admission != workerapi.AdmissionStopped || !reflect.DeepEqual(h.Reasons, []string{workerapi.ReasonFatal}) {
		t.Fatalf("%+v", h)
	}
	e.journal.Close()
}
