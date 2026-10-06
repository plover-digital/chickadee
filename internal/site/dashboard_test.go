package site

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDashboardWorkflowContainsOnlyEnabledQueues(t *testing.T) {
	s := fixture(t)
	s.sessions["session"] = session{User: User{7, "tester"}, Token: "fixture", CSRF: "csrf", Expires: time.Now().Add(time.Hour)}
	s.enrollments = []Enrollment{{ID: "request", User: User{7, "tester"}, InstallationID: 8, Repository: Repository{ID: 99, Name: "tester/repo"}, Status: "active", Queues: []string{"chickadee", "chickadee-medium-ubuntu-2404"}, EnabledQueues: []string{"chickadee"}, DesiredState: "active"}}
	s.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/user/installations/") {
			return response(`{"total_count":1,"repositories":[{"id":99,"full_name":"tester/repo","permissions":{"admin":true}}]}`), nil
		}
		return response(`{"total_count":1,"installations":[{"id":8,"app_id":42,"account":{"id":7,"login":"tester","type":"User"}}]}`), nil
	})
	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-chickadee-session", Value: "session"})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "runs-on: chickadee") || !strings.Contains(body, ".github/workflows/chickadee.yml") {
		t.Fatal("active first-job instructions missing")
	}
	if strings.Contains(body, `<option value="chickadee-medium-ubuntu-2404"`) || strings.Contains(body, "runs-on: chickadee-medium-ubuntu-2404") {
		t.Fatal("unapproved queue offered for workflow")
	}
	if !strings.Contains(body, "Jobs can wait for capacity") {
		t.Fatal("enabled status implies idle capacity")
	}
	s.enrollments[0].Status = "pending"
	s.enrollments[0].EnabledQueues = nil
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "workflow-queue") || !strings.Contains(rec.Body.String(), "awaiting beta approval") {
		t.Fatal("pending enrollment offers executable workflow")
	}
}
