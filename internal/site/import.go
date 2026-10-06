package site

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)

// importEnrollment accepts only already verified operator metadata on the
// private Unix admin listener. It never overwrites a customer's existing request.
func (s *Server) importEnrollment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var entry Enrollment
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&entry) != nil || decoder.Decode(new(any)) != io.EOF || entry.User.ID <= 0 || entry.User.Login == "" || entry.InstallationID <= 0 || entry.Account.ID <= 0 || entry.Account.Login == "" || entry.Repository.ID <= 0 || !repositoryName.MatchString(entry.Repository.Name) || !strings.EqualFold(strings.Split(entry.Repository.Name, "/")[0], entry.Account.Login) || entry.Status != "active" || len(entry.Message) > 500 || !validUsage(entry.Usage, time.Now()) {
		http.Error(w, "Invalid verified metadata", 400)
		return
	}
	if entry.Account.Type == "User" {
		if entry.Account.ID != entry.User.ID {
			http.Error(w, "Personal installation must belong to user", 400)
			return
		}
		entry.Scope = "repository"
	} else if entry.Account.Type == "Organization" {
		entry.Scope = "organization"
	} else {
		http.Error(w, "Invalid account type", 400)
		return
	}
	if !validWorkflowAccess(entry.EnabledWorkflowAccess) {
		http.Error(w, "Invalid workflow access", 400)
		return
	}
	if entry.WorkflowPath != "" && !validWorkflowPath(entry.WorkflowPath) || entry.EnabledWorkflowPath != "" && !validWorkflowPath(entry.EnabledWorkflowPath) {
		http.Error(w, "Invalid workflow path", 400)
		return
	}
	if entry.EnabledWorkflowPath == "" {
		entry.EnabledWorkflowPath = entry.WorkflowPath
	}
	queues, e := requestedQueues(entry.Queues)
	if e != nil {
		http.Error(w, "Invalid requested queues", 400)
		return
	}
	entry.Queues = queues
	enabled := map[string]bool{}
	for _, q := range entry.EnabledQueues {
		allowed := false
		for _, requested := range queues {
			if q == requested {
				allowed = true
				break
			}
		}
		if !allowed || enabled[q] {
			http.Error(w, "Invalid enabled queues", 400)
			return
		}
		enabled[q] = true
	}
	if !enabled["chickadee"] {
		http.Error(w, "Default queue must be enabled", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.enrollments {
		if old.User.ID == entry.User.ID && old.InstallationID == entry.InstallationID && old.Repository.ID == entry.Repository.ID {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": old.ID})
			return
		}
	}
	if len(s.enrollments) >= 500 {
		http.Error(w, "Beta capacity reached", 503)
		return
	}
	entry.ID = random()
	entry.Created = time.Now().UTC()
	entry.Updated = entry.Created
	entry.DesiredState = "active"
	entries := append(append([]Enrollment(nil), s.enrollments...), entry)
	if s.saveLocked(entries) != nil {
		http.Error(w, "Import could not be saved", 500)
		return
	}
	s.enrollments = entries
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(201)
	_ = json.NewEncoder(w).Encode(map[string]string{"id": entry.ID})
}
