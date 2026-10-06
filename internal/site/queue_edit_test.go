package site

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestQueueEditReflectsSelectionAndRemovesOnlyUncheckedQueue(t *testing.T) {
	s := fixture(t)
	s.sessions["session"] = session{User: User{7, "tester"}, Token: "fixture", CSRF: "csrf", Expires: time.Now().Add(time.Hour)}
	s.enrollments = []Enrollment{{ID: "own", User: User{ID: 7}, InstallationID: 8, Repository: Repository{ID: 99, Name: "tester/repo"}, Status: "active", Queues: []string{"chickadee", "chickadee-small-rocky-102", "chickadee-medium-ubuntu-2404"}, EnabledQueues: []string{"chickadee", "chickadee-small-rocky-102", "chickadee-medium-ubuntu-2404"}, DesiredState: "active"}}
	s.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/user/installations/") {
			return response(`{"total_count":1,"repositories":[{"id":99,"full_name":"tester/repo","permissions":{"admin":true}}]}`), nil
		}
		return response(`{"total_count":1,"installations":[{"id":8,"app_id":42,"account":{"id":7,"login":"tester","type":"User"},"permissions":{"administration":"write"}}]}`), nil
	})
	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-chickadee-session", Value: "session"})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `value="chickadee-medium-ubuntu-2404" checked`) || !strings.Contains(w.Body.String(), `<details class="repository-setup">`) {
		t.Fatal("queue selection or collapsed repository missing")
	}
	if strings.Index(w.Body.String(), "Sign out") > strings.Index(w.Body.String(), "<main") {
		t.Fatal("sign out not in header")
	}
	form := url.Values{"csrf": {"csrf"}, "installation_id": {"8"}, "repository_id": {"99"}, "queue": {"chickadee-small-rocky-102"}}
	post := httptest.NewRequest("POST", "/enroll", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Origin", "https://chickadee.run")
	post.AddCookie(&http.Cookie{Name: "__Host-chickadee-session", Value: "session"})
	w = httptest.NewRecorder()
	s.ServeHTTP(w, post)
	if w.Code != 303 || len(s.enrollments[0].Queues) != 2 || s.enrollments[0].Queues[0] != "chickadee" || s.enrollments[0].Queues[1] != "chickadee-small-rocky-102" {
		t.Fatal("unchecked queue not removed or default lost")
	}
	if len(s.enrollments[0].EnabledQueues) != 3 || s.enrollments[0].Status != "active" {
		t.Fatal("request prematurely changes applied state")
	}
}
