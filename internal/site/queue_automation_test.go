package site

import (
	"bytes"
	"strings"
	"testing"
)

func TestQueueAutomationCopyPreservesAccountApprovalAndOptIn(t *testing.T) {
	s := fixture(t)
	p := page{Title: "Your repositories", User: &User{ID: 7}, Choices: []Choice{{Installation: Installation{ID: 8, Account: Account{Type: "User"}}, Repository: Repository{ID: 99, Name: "tester/repo"}}}, ExtraQueues: extraQueues}
	var out bytes.Buffer
	if e := s.templates.ExecuteTemplate(&out, "page.html", p); e != nil {
		t.Fatal(e)
	}
	body := out.String()
	if !strings.Contains(body, "Choose additional queues as needed") || !strings.Contains(body, "Beta access requires approval; approved accounts activate supported queues automatically") || strings.Contains(body, "Additional queues require approval") {
		t.Fatal("queue activation copy implies individual manual approval")
	}
	if strings.Contains(body, `checked`) {
		t.Fatal("new customer queues implicitly opted in")
	}
}
