package site

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Invalid browser bindings must fail before the one-time code leaves Chickadee.
func TestOAuthRejectsExpiredAndDifferentBrowser(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "different browser", true: "expired state"}[expired], func(t *testing.T) {
			s := fixture(t)
			login := httptest.NewRecorder()
			s.ServeHTTP(login, httptest.NewRequest("GET", "/login", nil))
			u, _ := url.Parse(login.Header().Get("Location"))
			state := u.Query().Get("state")
			cookie := login.Result().Cookies()[0]
			if expired {
				flow := s.states[state]
				flow.Expires = time.Now().Add(-time.Second)
				s.states[state] = flow
			} else {
				cookie.Value = "another-browser"
			}
			calls := 0
			s.http.Transport = transport(func(*http.Request) (*http.Response, error) {
				calls++
				return response(`{"access_token":"never-used"}`), nil
			})
			req := httptest.NewRequest("GET", "/auth/github/callback?code=one-time-code&state="+url.QueryEscape(state), nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != 400 || calls != 0 || len(s.sessions) != 0 {
				t.Fatal("invalid login exchanged code or created session")
			}
		})
	}
}

func TestOAuthFailuresDoNotExposeUpstreamCredentials(t *testing.T) {
	for _, tokenFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "identity unavailable", true: "token denied"}[tokenFails], func(t *testing.T) {
			s := fixture(t)
			login := httptest.NewRecorder()
			s.ServeHTTP(login, httptest.NewRequest("GET", "/login", nil))
			u, _ := url.Parse(login.Header().Get("Location"))
			s.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "github.com" {
					if tokenFails {
						return response(`{"error":"invalid_client","error_description":"fake-client-secret"}`), nil
					}
					return response(`{"access_token":"ghu_sensitive-fixture","refresh_token":"ghr_sensitive-fixture"}`), nil
				}
				res := response(`{"message":"ghu_sensitive-fixture"}`)
				res.StatusCode = 401
				return res, nil
			})
			req := httptest.NewRequest("GET", "/auth/github/callback?code=test&state="+url.QueryEscape(u.Query().Get("state")), nil)
			req.AddCookie(login.Result().Cookies()[0])
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != 502 || len(s.sessions) != 0 {
				t.Fatal("failed login created session")
			}
			body := rec.Body.String()
			for _, secret := range []string{"fake-client-secret", "ghu_sensitive-fixture", "ghr_sensitive-fixture"} {
				if strings.Contains(body, secret) {
					t.Fatal("upstream credential reflected")
				}
			}
		})
	}
}

func TestOAuthUsesConfiguredCallbackWithoutLegacyScopes(t *testing.T) {
	s := fixture(t)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/login?redirect_uri=https://evil.example", nil))
	u, _ := url.Parse(rec.Header().Get("Location"))
	q := u.Query()
	if u.Host != "github.com" || u.Path != "/login/oauth/authorize" || q.Get("redirect_uri") != "https://chickadee.run/auth/github/callback" || q.Get("scope") != "" || q.Get("state") == "" {
		t.Fatal("authorization endpoint or callback/scopes invalid")
	}
}

func TestChoicesRequireRepositoryAdministrationAndPersonalOwner(t *testing.T) {
	for _, tc := range []struct {
		name, accountType string
		accountID         int
		admin             bool
		want              int
	}{
		{"personal owner", "User", 7, true, 1},
		{"personal collaborator", "User", 8, true, 0},
		{"organization reader", "Organization", 8, false, 0},
		{"organization administrator", "Organization", 8, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t)
			s.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
				if strings.HasPrefix(r.URL.Path, "/user/installations/") {
					return response(fmt.Sprintf(`{"total_count":1,"repositories":[{"id":99,"full_name":"owner/repo","permissions":{"admin":%t}}]}`, tc.admin)), nil
				}
				return response(fmt.Sprintf(`{"total_count":1,"installations":[{"id":8,"app_id":42,"account":{"id":%d,"login":"owner","type":"%s"}}]}`, tc.accountID, tc.accountType)), nil
			})
			choices, e := s.choices(context.Background(), session{User: User{7, "tester"}, Token: "fixture"})
			if e != nil || len(choices) != tc.want {
				t.Fatalf("got %d choices, error %v", len(choices), e)
			}
		})
	}
}
