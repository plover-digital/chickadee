package site

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUbuntu2604DefaultAndOptInQueues(t *testing.T) {
	queues, e := requestedQueues([]string{"chickadee-small-ubuntu-2604", "chickadee-medium-ubuntu-2604"})
	if e != nil || len(queues) != 3 || queues[0] != "chickadee" {
		t.Fatal("new Ubuntu queues not opt in")
	}
	all := append([]string{"chickadee"}, extraQueues...)
	if q, e := requestedQueues(all); e != nil || len(q) != 7 {
		t.Fatal("full catalog rejected")
	}
	if _, e := requestedQueues(append(all, "chickadee")); e == nil {
		t.Fatal("oversized queue input accepted")
	}
	if _, e := requestedQueues([]string{"chickadee-large-ubuntu-2604"}); e == nil {
		t.Fatal("unavailable resource class accepted")
	}
	s := fixture(t)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if !strings.Contains(body, "defaults to medium / Ubuntu 26.04") || !strings.Contains(body, `id="label">runs-on: chickadee-medium-ubuntu-2604`) || strings.Contains(body, "Ubuntu 26.04, Rocky 9.8 and larger profiles are planned") {
		t.Fatal("default image copy inconsistent")
	}
}
