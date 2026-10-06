package site

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestOrganizationServicesShareOneUsageSnapshotAndHideUnverifiedRepos(t *testing.T) {
	now := time.Now().UTC()
	org := Account{ID: 10, Login: "example-org", Type: "Organization"}
	a := Enrollment{ID: "a", User: User{ID: 7}, InstallationID: 8, Account: org, Repository: Repository{ID: 1, Name: "example-org/first"}, Scope: "organization", Status: "active", Updated: now.Add(-time.Hour), EnabledQueues: []string{"chickadee"}, Usage: []UsageDay{{Date: now.Format("2006-01-02"), VMSeconds: 60, VMs: 1}}}
	b := a
	b.ID = "b"
	b.Repository = Repository{ID: 2, Name: "example-org/second"}
	b.Updated = now
	b.Usage = []UsageDay{{Date: now.Format("2006-01-02"), VMSeconds: 120, VMs: 2}}
	hidden := b
	hidden.ID = "hidden"
	hidden.Repository = Repository{ID: 3, Name: "example-org/private-lost-access"}
	hidden.Status = "permission-required"
	hidden.Usage = nil
	hidden.EnabledQueues = nil
	p := page{Title: "Your repositories", User: &User{ID: 7}, Enrollments: []Enrollment{a, b, hidden}, Choices: []Choice{{Installation: Installation{ID: 8, Account: org}, Repository: a.Repository}, {Installation: Installation{ID: 8, Account: org}, Repository: b.Repository}}}
	services := p.Services()
	if len(services) != 1 || len(services[0].Repositories) != 2 || services[0].UsageChart().TotalMinutes != "2.0" {
		t.Fatal("organization service duplicated or usage summed")
	}
	if len(p.SetupChoices()) != 1 {
		t.Fatal("organization queue editors duplicated")
	}
	s := fixture(t)
	var out bytes.Buffer
	if e := s.templates.ExecuteTemplate(&out, "page.html", p); e != nil {
		t.Fatal(e)
	}
	html := out.String()
	if strings.Count(html, `class="usage-panel"`) != 1 || strings.Count(html, `aria-label="Runners for example-org"`) != 1 || strings.Contains(html, "private-lost-access") {
		t.Fatal("duplicated org UI or hidden repository exposed")
	}
	p.Enrollments[0].Account.Type = "User"
	p.Enrollments[1].Account.Type = "User"
	p.Enrollments = p.Enrollments[:2]
	p.Choices[0].Installation.Account.Type = "User"
	p.Choices[1].Installation.Account.Type = "User"
	if len(p.Services()) != 2 || len(p.SetupChoices()) != 2 {
		t.Fatal("personal repository services merged")
	}
}
