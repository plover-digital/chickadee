package site

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOperatorRepositoryWorkflowAccessIsExplicitAndPreserved(t *testing.T) {
	s := fixture(t)
	s.enrollments = []Enrollment{{ID: "request", Queues: []string{"chickadee"}, Status: "pending", DesiredState: "active", Account: Account{Type: "Organization"}, WorkflowPath: ".github/workflows/build.yml"}}
	post := func(mode string) int {
		w := httptest.NewRecorder()
		s.AdminHandler().ServeHTTP(w, httptest.NewRequest("POST", "/status", strings.NewReader(`{"id":"request","status":"active","enabled_queues":["chickadee"],"enabled_workflow_path":"","enabled_workflow_access":"`+mode+`"}`)))
		return w.Code
	}
	if post("all") != 400 {
		t.Fatal("invalid workflow access accepted")
	}
	if post("repository") != 204 || s.enrollments[0].WorkflowChangesPending() {
		t.Fatal("repository approval still claims main-only pending access")
	}
	w := httptest.NewRecorder()
	s.AdminHandler().ServeHTTP(w, httptest.NewRequest("POST", "/status", strings.NewReader(`{"id":"request","status":"active","enabled_queues":["chickadee"]}`)))
	if w.Code != 204 || s.enrollments[0].EnabledWorkflowAccess != "repository" {
		t.Fatal("legacy relay omitted mode and erased approval")
	}
}
