// Package site implements a small GitHub App onboarding site, separate from VM control.
package site

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed assets/*
var assets embed.FS
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

type Config struct {
	PublicURL, AppSlug, ClientID, ClientSecret, StateDir string
	AppID                                                int64
}
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}
type Account struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}
type Installation struct {
	ID          int64             `json:"id"`
	AppID       int64             `json:"app_id"`
	Account     Account           `json:"account"`
	Permissions map[string]string `json:"permissions"`
}
type Repository struct {
	ID          int64           `json:"id"`
	Name        string          `json:"full_name"`
	Private     bool            `json:"private"`
	Permissions map[string]bool `json:"permissions"`
}
type Choice struct {
	Installation Installation
	Repository   Repository
}
type Enrollment struct {
	ID             string     `json:"id"`
	User           User       `json:"user"`
	InstallationID int64      `json:"installation_id"`
	Account        Account    `json:"account"`
	Repository     Repository `json:"repository"`
	Scope          string     `json:"scope"`
	Status         string     `json:"status"`
	Queues         []string   `json:"queues,omitempty"`
	EnabledQueues  []string   `json:"enabled_queues,omitempty"`
	DesiredState   string     `json:"desired_state,omitempty"`
	Message        string     `json:"message,omitempty"`
	Updated        time.Time  `json:"updated_at,omitempty"`
	Created        time.Time  `json:"created_at"`
	Usage          []UsageDay `json:"usage,omitempty"`
}
type oauthState struct {
	Cookie, Verifier string
	Expires          time.Time
}
type session struct {
	User        User
	Token, CSRF string
	Expires     time.Time
}
type Server struct {
	cfg         Config
	templates   *template.Template
	mux         *http.ServeMux
	http        *http.Client
	api, oauth  string
	mu          sync.Mutex
	states      map[string]oauthState
	sessions    map[string]session
	enrollments []Enrollment
}
type page struct {
	Title, Message, CSRF string
	User                 *User
	Choices              []Choice
	Enrollments          []Enrollment
	ExtraQueues          []string
	LoginReady           bool
}

func New(c Config) (*Server, error) {
	u, e := url.Parse(c.PublicURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("public URL must be an origin")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return nil, errors.New("public URL requires HTTPS")
	}
	if !slugPattern.MatchString(c.AppSlug) || !filepath.IsAbs(c.StateDir) || filepath.Clean(c.StateDir) != c.StateDir || c.StateDir == "/" {
		return nil, errors.New("valid App slug and dedicated absolute state directory required")
	}
	if c.ClientSecret != "" && (c.ClientID == "" || c.AppID <= 0) {
		return nil, errors.New("OAuth requires App/client identity")
	}
	if e = os.MkdirAll(c.StateDir, 0700); e != nil {
		return nil, e
	}
	st, e := os.Lstat(c.StateDir)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("site state directory must be private and not a symlink")
	}
	t, e := template.ParseFS(assets, "assets/*.html")
	if e != nil {
		return nil, e
	}
	s := &Server{cfg: c, templates: t, mux: http.NewServeMux(), http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, api: "https://api.github.com", oauth: "https://github.com", states: map[string]oauthState{}, sessions: map[string]session{}}
	b, e := os.ReadFile(filepath.Join(c.StateDir, "enrollments.json"))
	if e == nil {
		if len(b) > 2<<20 || json.Unmarshal(b, &s.enrollments) != nil || len(s.enrollments) > 500 {
			return nil, errors.New("invalid enrollment store")
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	s.mux.HandleFunc("GET /{$}", s.home)
	s.mux.HandleFunc("GET /login", s.login)
	s.mux.HandleFunc("GET /auth/github/callback", s.callback)
	s.mux.HandleFunc("GET /install", s.install)
	s.mux.HandleFunc("GET /setup", s.setup)
	s.mux.HandleFunc("GET /dashboard", s.dashboard)
	s.mux.HandleFunc("POST /enroll", s.enroll)
	s.mux.HandleFunc("POST /logout", s.logout)
	s.mux.HandleFunc("POST /manage", s.manage)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	files, _ := fs.Sub(assets, "assets")
	s.mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(files))))
	return s, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}
func random() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("random source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func (s *Server) ready() bool { return s.cfg.ClientSecret != "" }
func (s *Server) cookieName(name string) string {
	if strings.HasPrefix(s.cfg.PublicURL, "https://") {
		return "__Host-" + name
	}
	return name
}
func (s *Server) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(name), Value: value, Path: "/", MaxAge: age, HttpOnly: true, Secure: strings.HasPrefix(s.cfg.PublicURL, "https://"), SameSite: http.SameSiteLaxMode})
}
func (s *Server) pruneLocked() {
	now := time.Now()
	for k, v := range s.states {
		if now.After(v.Expires) {
			delete(s.states, k)
		}
	}
	for k, v := range s.sessions {
		if now.After(v.Expires) {
			delete(s.sessions, k)
		}
	}
}
func (s *Server) current(r *http.Request) (string, session, bool) {
	c, e := r.Cookie(s.cookieName("chickadee-session"))
	if e != nil {
		return "", session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	v, ok := s.sessions[c.Value]
	return c.Value, v, ok
}
func (s *Server) render(w http.ResponseWriter, p page) {
	p.LoginReady = s.ready()
	var b bytes.Buffer
	if e := s.templates.ExecuteTemplate(&b, "page.html", p); e != nil {
		http.Error(w, "Page unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b.Bytes())
}
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	_, v, ok := s.current(r)
	p := page{Title: "Ephemeral GitHub Actions runners"}
	if ok {
		p.User = &v.User
		p.CSRF = v.CSRF
	}
	s.render(w, p)
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.ready() {
		s.render(w, page{Title: "GitHub sign-in", Message: "GitHub sign-in is being configured. You can install the App when GitHub makes it publicly available; runner activation is a separate beta step."})
		return
	}
	state, nonce, verifier := random(), random(), random()
	s.mu.Lock()
	s.pruneLocked()
	if len(s.states) >= 1024 {
		s.mu.Unlock()
		http.Error(w, "Try again shortly", 503)
		return
	}
	s.states[state] = oauthState{nonce, verifier, time.Now().Add(10 * time.Minute)}
	s.mu.Unlock()
	s.cookie(w, "chickadee-oauth", nonce, 600)
	digest := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {s.cfg.ClientID}, "redirect_uri": {s.cfg.PublicURL + "/auth/github/callback"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}}
	http.Redirect(w, r, s.oauth+"/login/oauth/authorize?"+q.Encode(), http.StatusFound)
}
func (s *Server) fetch(ctx context.Context, path, token string, out any) error {
	req, e := http.NewRequestWithContext(ctx, "GET", s.api+path, nil)
	if e != nil {
		return errors.New("GitHub request unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "chickadee-onboarding")
	res, e := s.http.Do(req)
	if e != nil {
		return errors.New("GitHub unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("GitHub authorization unavailable")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if e != nil || len(b) > 2<<20 || json.Unmarshal(b, out) != nil {
		return errors.New("GitHub response invalid")
	}
	return nil
}
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
	cookie, e := r.Cookie(s.cookieName("chickadee-oauth"))
	s.mu.Lock()
	s.pruneLocked()
	flow, ok := s.states[state]
	delete(s.states, state)
	s.mu.Unlock()
	s.cookie(w, "chickadee-oauth", "", -1)
	if !s.ready() || !ok || e != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(flow.Cookie)) != 1 || code == "" || len(code) > 1024 {
		http.Error(w, "Sign-in expired or invalid. Start again from the website.", 400)
		return
	}
	data := url.Values{"client_id": {s.cfg.ClientID}, "client_secret": {s.cfg.ClientSecret}, "code": {code}, "redirect_uri": {s.cfg.PublicURL + "/auth/github/callback"}, "code_verifier": {flow.Verifier}}
	req, _ := http.NewRequestWithContext(r.Context(), "POST", s.oauth+"/login/oauth/access_token", strings.NewReader(data.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, e := s.http.Do(req)
	if e != nil {
		http.Error(w, "GitHub sign-in unavailable", 502)
		return
	}
	defer res.Body.Close()
	var result struct {
		Token string `json:"access_token"`
		Error string `json:"error"`
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, 65537))
	if e != nil || res.StatusCode != 200 || len(b) > 65536 || json.Unmarshal(b, &result) != nil || result.Token == "" || result.Error != "" {
		http.Error(w, "GitHub sign-in failed. Start again.", 502)
		return
	}
	var user User
	if s.fetch(r.Context(), "/user", result.Token, &user) != nil || user.ID <= 0 || user.Login == "" {
		http.Error(w, "GitHub identity unavailable", 502)
		return
	}
	sid := random()
	s.mu.Lock()
	s.pruneLocked()
	if len(s.sessions) >= 1024 {
		s.mu.Unlock()
		http.Error(w, "Try again shortly", 503)
		return
	}
	s.sessions[sid] = session{user, result.Token, random(), time.Now().Add(time.Hour)}
	s.mu.Unlock()
	s.cookie(w, "chickadee-session", sid, 3600)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}
func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "https://github.com/apps/"+s.cfg.AppSlug+"/installations/new", http.StatusFound)
}
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	// A callback query is not proof of installation or ownership. The dashboard
	// checks installation access through GitHub using the authenticated user token.
	if _, _, ok := s.current(r); ok {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	s.render(w, page{Title: "Connect your repositories", Message: "Continue with GitHub to verify your identity and see the repositories available to this App. Installing the App does not automatically activate a runner pool during the beta."})
}
func (s *Server) choices(ctx context.Context, v session) ([]Choice, error) {
	var list struct {
		Total         int            `json:"total_count"`
		Installations []Installation `json:"installations"`
	}
	if e := s.fetch(ctx, "/user/installations?per_page=100", v.Token, &list); e != nil {
		return nil, e
	}
	if list.Total > 100 {
		return nil, errors.New("installation list exceeds beta limit")
	}
	var out []Choice
	for _, i := range list.Installations {
		if i.AppID != s.cfg.AppID || i.ID <= 0 || i.Account.ID <= 0 || (i.Account.Type != "User" && i.Account.Type != "Organization") || (i.Account.Type == "User" && i.Account.ID != v.User.ID) {
			continue
		}
		var repos struct {
			Total        int          `json:"total_count"`
			Repositories []Repository `json:"repositories"`
		}
		if e := s.fetch(ctx, fmt.Sprintf("/user/installations/%d/repositories?per_page=100", i.ID), v.Token, &repos); e != nil {
			return nil, e
		}
		if repos.Total > 100 || len(out)+len(repos.Repositories) > 500 {
			return nil, errors.New("repository list exceeds beta limit")
		}
		for _, repo := range repos.Repositories {
			if repo.ID > 0 && repo.Permissions["admin"] {
				out = append(out, Choice{i, repo})
			}
		}
	}
	return out, nil
}
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	_, v, ok := s.current(r)
	if !ok {
		http.Redirect(w, r, "/login", 303)
		return
	}
	choices, e := s.choices(r.Context(), v)
	p := page{Title: "Your repositories", User: &v.User, CSRF: v.CSRF, Choices: choices, ExtraQueues: extraQueues}
	if e != nil {
		p.Message = "GitHub access could not be verified. Sign in again, or check your App installation permissions."
	}
	s.mu.Lock()
	for _, entry := range s.enrollments {
		if entry.User.ID == v.User.ID {
			p.Enrollments = append(p.Enrollments, entry)
		}
	}
	s.mu.Unlock()
	s.render(w, p)
}
func (s *Server) postSession(w http.ResponseWriter, r *http.Request) (string, session, bool) {
	sid, v, ok := s.current(r)
	if !ok {
		http.Error(w, "Sign in again", 401)
		return "", v, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil || subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(v.CSRF)) != 1 || r.Header.Get("Origin") != s.cfg.PublicURL {
		http.Error(w, "Invalid request", 403)
		return "", v, false
	}
	return sid, v, true
}

var extraQueues = []string{"chickadee-small-rocky-102", "chickadee-medium-rocky-102", "chickadee-small-ubuntu-2404", "chickadee-medium-ubuntu-2404"}

func requestedQueues(values []string) ([]string, error) {
	if len(values) > 5 {
		return nil, errors.New("too many queues")
	}
	queues := []string{"chickadee"}
	for _, q := range values {
		if q == "chickadee" {
			continue
		}
		valid := false
		for _, allowed := range extraQueues {
			if q == allowed {
				valid = true
				break
			}
		}
		if !valid {
			return nil, errors.New("unknown queue")
		}
		duplicate := false
		for _, existing := range queues {
			if existing == q {
				duplicate = true
				break
			}
		}
		if !duplicate {
			queues = append(queues, q)
		}
	}
	return queues, nil
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	_, v, ok := s.postSession(w, r)
	if !ok {
		return
	}
	queues, err := requestedQueues(r.PostForm["queue"])
	if err != nil {
		http.Error(w, "Invalid queue selection", 400)
		return
	}
	iid, e1 := strconv.ParseInt(r.FormValue("installation_id"), 10, 64)
	rid, e2 := strconv.ParseInt(r.FormValue("repository_id"), 10, 64)
	if e1 != nil || e2 != nil || iid <= 0 || rid <= 0 {
		http.Error(w, "Invalid repository selection", 400)
		return
	}
	choices, e := s.choices(r.Context(), v)
	if e != nil {
		http.Error(w, "GitHub access could not be verified", 502)
		return
	}
	var choice *Choice
	for _, c := range choices {
		if c.Installation.ID == iid && c.Repository.ID == rid {
			copy := c
			choice = &copy
			break
		}
	}
	if choice == nil {
		http.Error(w, "Repository not authorized", 403)
		return
	}
	permissions := choice.Installation.Permissions
	if choice.Installation.Account.Type == "User" && permissions["administration"] != "write" || choice.Installation.Account.Type == "Organization" && permissions["organization_self_hosted_runners"] != "write" {
		http.Error(w, "App runner-management permission needs approval", 403)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, entry := range s.enrollments {
		if entry.InstallationID == iid && entry.Repository.ID == rid && entry.User.ID == v.User.ID {
			merged := append([]string(nil), entry.Queues...)
			if len(merged) == 0 {
				merged = []string{"chickadee"}
			}
			for _, queue := range queues {
				found := false
				for _, old := range merged {
					if old == queue {
						found = true
						break
					}
				}
				if !found {
					merged = append(merged, queue)
				}
			}
			entries := append([]Enrollment(nil), s.enrollments...)
			entries[i].Queues = merged
			if e = s.saveLocked(entries); e != nil {
				http.Error(w, "Request could not be saved", 500)
				return
			}
			s.enrollments = entries
			http.Redirect(w, r, "/dashboard", 303)
			return
		}
	}
	if len(s.enrollments) >= 500 {
		http.Error(w, "Beta capacity reached", 503)
		return
	}
	scope := "repository"
	if choice.Installation.Account.Type == "Organization" {
		scope = "organization"
	}
	entries := append(append([]Enrollment(nil), s.enrollments...), Enrollment{ID: random(), User: v.User, InstallationID: iid, Account: choice.Installation.Account, Repository: choice.Repository, Scope: scope, Status: "pending", Queues: queues, DesiredState: "active", Created: time.Now().UTC()})
	if e = s.saveLocked(entries); e != nil {
		http.Error(w, "Request could not be saved", 500)
		return
	}
	s.enrollments = entries
	http.Redirect(w, r, "/dashboard", 303)
}
func (s *Server) saveLocked(entries []Enrollment) error {
	b, e := json.MarshalIndent(entries, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(s.cfg.StateDir, ".enrollments-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(f.Name(), filepath.Join(s.cfg.StateDir, "enrollments.json")); e != nil {
		return e
	}
	dir, e := os.Open(s.cfg.StateDir)
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	sid, _, ok := s.postSession(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	delete(s.sessions, sid)
	s.mu.Unlock()
	s.cookie(w, "chickadee-session", "", -1)
	http.Redirect(w, r, "/", 303)
}
