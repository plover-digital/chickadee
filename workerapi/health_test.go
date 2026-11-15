package workerapi

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type healthBackend struct {
	*testBackend
	health Health
}

func (b healthBackend) Health() (Health, error) { return b.health, nil }

func healthFixture(t *testing.T, h Health) *fixture {
	return newFixtureHandler(t, func(i Identity, b *testBackend) http.Handler {
		return NewHandler(i, healthBackend{b, h})
	})
}

func TestHealthRoundTrip(t *testing.T) {
	want := Health{Admission: AdmissionRefusing, Reasons: []string{ReasonDiskReserve, ReasonJournalFull}, Journal: JournalHealth{Records: 1024, MaxRecords: 1024}, Disk: &DiskHealth{AvailableBytes: 1 << 30, RequiredBytes: 2 << 30}}
	f := healthFixture(t, want)
	got, err := f.client.Health(context.Background())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
}

func TestHealthUnsupportedOnOlderWorker(t *testing.T) {
	f := newFixture(t)
	_, err := f.client.Health(context.Background())
	if !errors.Is(err, ErrUnsupported) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnavailable) {
		t.Fatal("older worker not reported as unsupported", err)
	}
	if f.backend.calls != 0 {
		t.Fatal("unsupported route reached the engine")
	}
}

func TestHealthRequestIsStrict(t *testing.T) {
	f := healthFixture(t, Health{Admission: AdmissionOpen, Reasons: []string{}, Journal: JournalHealth{MaxRecords: 1}})
	code, data := f.raw(t, f.broker, `{"version":1,"identity":{"worker_id":"worker-one","broker_id":"broker-one","generation":1},"assignment_id":"x"}`, "/v1/health")
	if code != http.StatusBadRequest || !strings.Contains(data, "invalid") {
		t.Fatal("health accepted extra request fields", code, data)
	}
	code, _ = f.raw(t, f.other, `{"version":1,"identity":{"worker_id":"worker-one","broker_id":"broker-one","generation":1}}`, "/v1/health")
	if code != http.StatusForbidden {
		t.Fatal("wrong broker reached health", code)
	}
}

func TestInvalidHealthRejected(t *testing.T) {
	for name, h := range map[string]Health{
		"open with reason":   {Admission: AdmissionOpen, Reasons: []string{ReasonFatal}, Journal: JournalHealth{MaxRecords: 1}},
		"refusing no reason": {Admission: AdmissionRefusing, Reasons: []string{}, Journal: JournalHealth{MaxRecords: 1}},
		"unknown admission":  {Admission: "maybe", Reasons: []string{}, Journal: JournalHealth{MaxRecords: 1}},
		"free text reason":   {Admission: AdmissionStopped, Reasons: []string{"/var/lib/state is full"}, Journal: JournalHealth{MaxRecords: 1}},
		"duplicate reason":   {Admission: AdmissionStopped, Reasons: []string{ReasonFatal, ReasonFatal}, Journal: JournalHealth{MaxRecords: 1}},
		"nil reasons":        {Admission: AdmissionOpen, Journal: JournalHealth{MaxRecords: 1}},
		"journal overflow":   {Admission: AdmissionOpen, Reasons: []string{}, Journal: JournalHealth{Records: 2, MaxRecords: 1}},
		"negative disk":      {Admission: AdmissionOpen, Reasons: []string{}, Journal: JournalHealth{MaxRecords: 1}, Disk: &DiskHealth{AvailableBytes: -1}},
	} {
		t.Run(name, func(t *testing.T) {
			f := healthFixture(t, h)
			if _, err := f.client.Health(context.Background()); err == nil {
				t.Fatal("invalid health accepted")
			}
		})
	}
}
