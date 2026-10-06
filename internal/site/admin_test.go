package site

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOperatorStatusNeverPublicAndApprovesOnlyRequestedQueues(t *testing.T) {
	s := fixture(t)
	s.enrollments = []Enrollment{{ID: "request", User: User{ID: 7}, Queues: []string{"chickadee"}, Status: "pending", DesiredState: "active"}}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/enrollments", nil))
	if rec.Code != 404 {
		t.Fatal("admin exposed publicly")
	}
	post := func(body string) int {
		w := httptest.NewRecorder()
		s.AdminHandler().ServeHTTP(w, httptest.NewRequest("POST", "/status", strings.NewReader(body)))
		return w.Code
	}
	if post(`{"id":"request","status":"active","enabled_queues":["chickadee","chickadee-medium-ubuntu-2404"]}`) != 400 {
		t.Fatal("unrequested queue enabled")
	}
	if post(`{"id":"request","status":"active","enabled_queues":["chickadee"]}`) != 204 || s.enrollments[0].Status != "active" {
		t.Fatal("approval failed")
	}
	s.enrollments[0].DesiredState = "paused"
	if post(`{"id":"request","status":"active","enabled_queues":["chickadee"]}`) != 409 {
		t.Fatal("activation ignored pause request")
	}
	if post(`{"id":"request","status":"paused","enabled_queues":[]}`) != 204 {
		t.Fatal("pause acknowledgement failed")
	}
}
func TestCustomerControlsRequireOwnershipAndAwaitReconciliation(t *testing.T) {
	s := fixture(t)
	s.sessions["session"] = session{User: User{ID: 7}, CSRF: "csrf", Expires: time.Now().Add(time.Hour)}
	s.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/user/installations/") {
			return response(`{"total_count":1,"repositories":[{"id":99,"full_name":"tester/repo","permissions":{"admin":true}}]}`), nil
		}
		return response(`{"total_count":1,"installations":[{"id":8,"app_id":42,"account":{"id":7,"type":"User"}}]}`), nil
	})
	s.enrollments = []Enrollment{{ID: "mine", InstallationID: 8, Repository: Repository{ID: 99}, User: User{ID: 7}, Status: "active", DesiredState: "active"}, {ID: "other", User: User{ID: 8}, Status: "active"}}
	post := func(id string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/manage", strings.NewReader("id="+id+"&action=paused&csrf=csrf"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://chickadee.run")
		r.AddCookie(&http.Cookie{Name: "__Host-chickadee-session", Value: "session"})
		s.ServeHTTP(w, r)
		return w.Code
	}
	if post("other") != 403 {
		t.Fatal("another user paused")
	}
	if post("mine") != 303 || s.enrollments[0].DesiredState != "paused" || s.enrollments[0].Status != "active" {
		t.Fatal("pause claimed applied before reconciliation")
	}
}
