package site

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUsageBoundsDatesAndChartValues(t *testing.T) {
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	valid := []UsageDay{{Date: "2026-10-05", VMSeconds: 60, VMs: 1}, {Date: "2026-10-06", VMSeconds: 120, VMs: 2}}
	if !validUsage(valid, now) {
		t.Fatal("valid days rejected")
	}
	c := (Enrollment{Usage: valid}).UsageChart()
	if c.TotalMinutes != "3.0" || c.TotalVMs != 3 || c.Days[0].Height != 50 || c.Days[1].Height != 100 {
		t.Fatal("incorrect graph values")
	}
	for _, day := range []UsageDay{{Date: "2026-10-07"}, {Date: "2026-09-29"}, {Date: "2026-10-06", VMSeconds: -1}, {Date: "2026-10-06", VMSeconds: 86400*32 + 1}, {Date: "2026-10-06", VMSeconds: math.Inf(1)}, {Date: "2026-10-06", VMs: 10001}, {Date: "2026-10-06", VMs: -1}, {Date: "bad"}} {
		if validUsage([]UsageDay{day}, now) {
			t.Fatalf("invalid day accepted: %+v", day)
		}
	}
	if validUsage([]UsageDay{valid[0], valid[0]}, now) || validUsage([]UsageDay{valid[1], valid[0]}, now) {
		t.Fatal("duplicate or unsorted dates accepted")
	}
}

func TestUsageOperatorUpdateAndCustomerPrivacy(t *testing.T) {
	s := fixture(t)
	today := time.Now().UTC().Format("2006-01-02")
	s.enrollments = []Enrollment{{ID: "own", User: User{ID: 7}, Repository: Repository{Name: "tester/repo"}, Status: "pending", Queues: []string{"chickadee"}, DesiredState: "active"}, {ID: "other", User: User{ID: 8}, Repository: Repository{Name: "private-owner/hidden"}, Usage: []UsageDay{{Date: today, VMSeconds: 59940, VMs: 99}}}}
	post := func(days []UsageDay) int {
		b, _ := json.Marshal(map[string]any{"id": "own", "status": "active", "enabled_queues": []string{"chickadee"}, "usage": days})
		w := httptest.NewRecorder()
		s.AdminHandler().ServeHTTP(w, httptest.NewRequest("POST", "/status", strings.NewReader(string(b))))
		return w.Code
	}
	if post([]UsageDay{{Date: today, VMSeconds: -1}}) != 400 || len(s.enrollments[0].Usage) != 0 {
		t.Fatal("invalid usage persisted")
	}
	if post([]UsageDay{{Date: today, VMSeconds: 120, VMs: 1}}) != 204 {
		t.Fatal("operator usage update failed")
	}
	s.sessions["session"] = session{User: User{7, "tester"}, Token: "fixture", CSRF: "csrf", Expires: time.Now().Add(time.Hour)}
	s.http.Transport = transport(func(*http.Request) (*http.Response, error) {
		return response(`{"total_count":0,"installations":[]}`), nil
	})
	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-chickadee-session", Value: "session"})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "2.0 VM minutes") || !strings.Contains(body, "Daily usage values") || !strings.Contains(body, "not job execution time or billable usage") {
		t.Fatal("real graph or explanatory values missing")
	}
	if strings.Contains(body, "private-owner/hidden") || strings.Contains(body, "999.0 VM minutes") {
		t.Fatal("another user's usage exposed")
	}
	s.enrollments[0].Usage = nil
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "No recorded VM usage yet") || strings.Contains(w.Body.String(), `class="chart-bar"`) {
		t.Fatal("empty history fabricated bars")
	}
}
