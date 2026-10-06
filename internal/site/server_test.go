package site

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixture(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	s, e := New(Config{PublicURL: "https://chickadee.run", AppSlug: "chickadee-run", ClientID: "Iv1.test", ClientSecret: "fake-client-secret", AppID: 42, StateDir: dir})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func response(data string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data))}
}
func TestOAuthPKCEBrowserBindingAndReplay(t *testing.T) {
	s := fixture(t)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/login", nil))
	u, _ := url.Parse(rec.Header().Get("Location"))
	q := u.Query()
	state := q.Get("state")
	flow := s.states[state]
	digest := sha256.Sum256([]byte(flow.Verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) || q.Get("code_challenge_method") != "S256" {
		t.Fatal("missing PKCE")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || !strings.HasPrefix(cookies[0].Name, "__Host-") {
		t.Fatal("unsafe OAuth cookie")
	}
	calls := 0
	s.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "github.com" {
			if r.Method != "POST" {
				t.Fatal("token sent in URL")
			}
			_ = r.ParseForm()
			if r.FormValue("code_verifier") != flow.Verifier || r.FormValue("client_secret") != "fake-client-secret" {
				t.Fatal("exchange missing verifier")
			}
			return response(`{"access_token":"ghu_test-never-persist","expires_in":28800}`), nil
		}
		return response(`{"id":7,"login":"tester"}`), nil
	})
	req := httptest.NewRequest("GET", "/auth/github/callback?code=test-code&state="+url.QueryEscape(state), nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 303 || rec.Header().Get("Location") != "/dashboard" || calls != 2 {
		t.Fatal("valid login failed")
	}
	cookie := rec.Result().Cookies()[1]
	_, user, ok := s.current(&http.Request{Header: http.Header{"Cookie": {cookie.String()}}})
	if !ok || user.User.ID != 7 {
		t.Fatal("session missing")
	}
	replay := httptest.NewRecorder()
	s.ServeHTTP(replay, req)
	if replay.Code != 400 || calls != 2 {
		t.Fatal("authorization replay accepted")
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/login", nil))
	u, _ = url.Parse(rec.Header().Get("Location"))
	missingCookie := httptest.NewRecorder()
	s.ServeHTTP(missingCookie, httptest.NewRequest("GET", "/auth/github/callback?code=test&state="+u.Query().Get("state"), nil))
	if missingCookie.Code != 400 {
		t.Fatal("login CSRF accepted")
	}
}
func TestEnrollmentVerifiesRepositoryAndNeverPersistsToken(t *testing.T) {
	s := fixture(t)
	s.sessions["session"] = session{User: User{7, "tester"}, Token: "ghu_private_test_token", CSRF: "csrf-value", Expires: time.Now().Add(time.Hour)}
	s.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer ghu_private_test_token" {
			t.Fatal("missing auth")
		}
		if strings.HasPrefix(r.URL.Path, "/user/installations/") {
			return response(`{"total_count":1,"repositories":[{"id":99,"full_name":"tester/repo","private":true}]}`), nil
		}
		return response(`{"total_count":1,"installations":[{"id":8,"app_id":42,"account":{"id":7,"login":"tester","type":"User"},"permissions":{"administration":"write"}}]}`), nil
	})
	post := func(repo, csrf, origin string) *httptest.ResponseRecorder {
		t.Helper()
		form := url.Values{"csrf": {csrf}, "installation_id": {"8"}, "repository_id": {repo}}
		req := httptest.NewRequest("POST", "/enroll", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		req.AddCookie(&http.Cookie{Name: "__Host-chickadee-session", Value: "session"})
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}
	if post("99", "csrf-value", "https://evil.example").Code != 403 || post("99", "wrong", "https://chickadee.run").Code != 403 || post("100", "csrf-value", "https://chickadee.run").Code != 403 {
		t.Fatal("unauthorized activation accepted")
	}
	if post("99", "csrf-value", "https://chickadee.run").Code != 303 {
		t.Fatal("valid request failed")
	}
	b, e := os.ReadFile(filepath.Join(s.cfg.StateDir, "enrollments.json"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "ghu_") || strings.Contains(string(b), "csrf-value") || strings.Contains(string(b), "client-secret") {
		t.Fatal("credential persisted")
	}
	var entries []Enrollment
	if json.Unmarshal(b, &entries) != nil || len(entries) != 1 || entries[0].Status != "pending" || entries[0].Scope != "repository" {
		t.Fatal("incorrect enrollment")
	}
	if post("99", "csrf-value", "https://chickadee.run").Code != 303 || len(s.enrollments) != 1 {
		t.Fatal("duplicate enrollment")
	}
	st, _ := os.Stat(filepath.Join(s.cfg.StateDir, "enrollments.json"))
	if st.Mode().Perm() != 0600 {
		t.Fatal("public metadata store")
	}
}
func TestSetupDoesNotTrustInstallationQuery(t *testing.T) {
	s := fixture(t)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/setup?installation_id=999999&setup_action=install", nil))
	if rec.Code != 200 || len(s.enrollments) != 0 || !strings.Contains(rec.Body.String(), "verify your identity") {
		t.Fatal("unverified callback accepted")
	}
}
func TestPageHeadersAndInstallTarget(t *testing.T) {
	s := fixture(t)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Continue with GitHub") || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("page/headers missing")
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/install", nil))
	if rec.Header().Get("Location") != "https://github.com/apps/chickadee-run/installations/new" {
		t.Fatal("wrong App")
	}
}
