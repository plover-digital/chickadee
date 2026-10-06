package site

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestWorkflowPathsAndServerBuiltMainRef(t *testing.T) {
	for _, path := range []string{".github/workflows/build.yml", ".github/workflows/ci-test.yaml"} {
		if !validWorkflowPath(path) {
			t.Fatal("valid workflow rejected")
		}
	}
	for _, path := range []string{"https://github.com/owner/repo/blob/main/.github/workflows/build.yml", ".github/workflows/../secret.yml", ".github/workflows/nested/build.yml", ".github/workflows/build.yml@refs/heads/evil", "build.yml", ".github/workflows/build.txt"} {
		if validWorkflowPath(path) {
			t.Fatalf("unsafe path accepted %q", path)
		}
	}
	e := Enrollment{Account: Account{Type: "Organization"}, Repository: Repository{Name: "example/verified-repo"}, WorkflowPath: ".github/workflows/build.yml"}
	if e.RequestedWorkflowRef() != "example/verified-repo/.github/workflows/build.yml@refs/heads/main" || !e.WorkflowChangesPending() {
		t.Fatal("workflow ref not pinned to verified repo and main")
	}
	e.EnabledWorkflowPath = e.WorkflowPath
	if e.WorkflowChangesPending() {
		t.Fatal("applied workflow still pending")
	}
}
func TestOrganizationEnrollmentRequiresExactWorkflowAndPendingNotProcessing(t *testing.T) {
	s := fixture(t)
	s.sessions["session"] = session{User: User{7, "tester"}, Token: "fixture", CSRF: "csrf", Expires: time.Now().Add(time.Hour)}
	s.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/user/installations/") {
			return response(`{"total_count":1,"repositories":[{"id":99,"full_name":"example/repo","permissions":{"admin":true}}]}`), nil
		}
		return response(`{"total_count":1,"installations":[{"id":8,"app_id":42,"account":{"id":10,"login":"example","type":"Organization"},"permissions":{"organization_self_hosted_runners":"write"}}]}`), nil
	})
	post := func(path string) int {
		f := url.Values{"csrf": {"csrf"}, "installation_id": {"8"}, "repository_id": {"99"}, "workflow_path": {path}}
		r := httptest.NewRequest("POST", "/enroll", strings.NewReader(f.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://chickadee.run")
		r.AddCookie(&http.Cookie{Name: "__Host-chickadee-session", Value: "session"})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	if post("") != 400 || post(".github/workflows/../oops.yml") != 400 || post(".github/workflows/build.yml") != 303 {
		t.Fatal("org workflow validation failed")
	}
	if s.enrollments[0].WorkflowPath != ".github/workflows/build.yml" || s.enrollments[0].EnabledWorkflowPath != "" {
		t.Fatal("workflow request applied without approval")
	}
	p := page{Title: "Your repositories", User: &User{ID: 7}, Enrollments: s.enrollments, Choices: []Choice{{Installation: Installation{ID: 8, Account: Account{Type: "Organization"}}, Repository: Repository{ID: 99}}}}
	var html bytes.Buffer
	if e := s.templates.ExecuteTemplate(&html, "page.html", p); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(html.String(), "activation is being processed") || strings.Contains(html.String(), "request to activate or resume is") {
		t.Fatal("unapproved request claims provisioning")
	}
	s.enrollments[0].Status = "approved"
	p.Enrollments = s.enrollments
	html.Reset()
	_ = s.templates.ExecuteTemplate(&html, "page.html", p)
	if !strings.Contains(html.String(), "Beta access is approved. Runner activation is being processed") {
		t.Fatal("approved processing stage missing")
	}
}
