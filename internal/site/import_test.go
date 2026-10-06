package site

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPrivateImportDeduplicatesWithoutOverridingCustomerPause(t *testing.T) {
	s := fixture(t)
	body := `{"user":{"id":7,"login":"tester"},"installation_id":8,"account":{"id":7,"login":"tester","type":"User"},"repository":{"id":99,"full_name":"tester/repo"},"status":"active","queues":["chickadee"],"enabled_queues":["chickadee"]}`
	post := func(body string) int {
		w := httptest.NewRecorder()
		s.AdminHandler().ServeHTTP(w, httptest.NewRequest("POST", "/import-enrollment", strings.NewReader(body)))
		return w.Code
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/import-enrollment", strings.NewReader(body)))
	if w.Code != 404 {
		t.Fatal("import exposed publicly")
	}
	if post(strings.Replace(body, `"id":7,"login":"tester","type"`, `"id":9,"login":"tester","type"`, 1)) != 400 {
		t.Fatal("nonowner personal account accepted")
	}
	if post(strings.Replace(body, `"status":"active"`, `"access_token":"secret","status":"active"`, 1)) != 400 {
		t.Fatal("credentials accepted as metadata")
	}
	if post(body) != 201 || len(s.enrollments) != 1 || s.enrollments[0].ID == "" {
		t.Fatal("verified import failed")
	}
	s.enrollments[0].DesiredState = "paused"
	if post(body) != 200 || len(s.enrollments) != 1 || s.enrollments[0].DesiredState != "paused" {
		t.Fatal("import overrode existing customer request")
	}
}

func TestDashboardHidesUsageAndEnabledWorkflowAfterAdminLoss(t *testing.T) {
	s := fixture(t)
	today := time.Now().UTC().Format("2006-01-02")
	s.sessions["session"] = session{User: User{7, "tester"}, Token: "fixture", CSRF: "csrf", Expires: time.Now().Add(time.Hour)}
	s.enrollments = []Enrollment{{ID: "own", User: User{ID: 7}, InstallationID: 8, Repository: Repository{ID: 99, Name: "tester/repo"}, Status: "active", Queues: []string{"chickadee"}, EnabledQueues: []string{"chickadee"}, Usage: []UsageDay{{Date: today, VMSeconds: 59940, VMs: 99}}}}
	s.http.Transport = transport(func(*http.Request) (*http.Response, error) {
		return response(`{"total_count":0,"installations":[]}`), nil
	})
	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-chickadee-session", Value: "session"})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	body := w.Body.String()
	if strings.Contains(body, "999.0") || strings.Contains(body, "workflow-queue") || strings.Contains(body, `class="chart-bar"`) {
		t.Fatal("revoked administrator can see usage or enabled workflow")
	}
	if !strings.Contains(body, "permission-required") || !strings.Contains(body, "administration access could not be verified") {
		t.Fatal("missing access guidance")
	}
	if len(s.enrollments[0].Usage) != 1 || len(s.enrollments[0].EnabledQueues) != 1 || s.enrollments[0].Status != "active" {
		t.Fatal("render changed operator state")
	}
}
