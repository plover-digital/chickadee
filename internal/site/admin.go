package site

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// AdminHandler is served only over an opt-in, private Unix socket. Never mount
// this handler on the public HTTP server or reverse proxy.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /enrollments", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(s.enrollments)
	})
	mux.HandleFunc("POST /status", func(w http.ResponseWriter, r *http.Request) {
		var update struct {
			ID            string     `json:"id"`
			Status        string     `json:"status"`
			EnabledQueues []string   `json:"enabled_queues"`
			Message       string     `json:"message"`
			Usage         []UsageDay `json:"usage"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&update) != nil || decoder.Decode(new(any)) != io.EOF || len(update.Message) > 500 || !validUsage(update.Usage, time.Now()) {
			http.Error(w, "Invalid update", 400)
			return
		}
		switch update.Status {
		case "pending", "active", "paused", "disconnected", "permission-required", "error":
		default:
			http.Error(w, "Invalid status", 400)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, entry := range s.enrollments {
			if entry.ID != update.ID {
				continue
			}
			requested := entry.Queues
			if len(requested) == 0 {
				requested = []string{"chickadee"}
			}
			if len(update.EnabledQueues) > len(requested) {
				http.Error(w, "Queue not requested", 400)
				return
			}
			seen := map[string]bool{}
			for _, queue := range update.EnabledQueues {
				allowed := false
				for _, q := range requested {
					if queue == q {
						allowed = true
						break
					}
				}
				if !allowed || seen[queue] {
					http.Error(w, "Queue not requested", 400)
					return
				}
				seen[queue] = true
			}
			if update.Status == "active" && (!seen["chickadee"] || entry.DesiredState == "paused" || entry.DesiredState == "disconnected") {
				http.Error(w, "Activation conflicts with requested state", 409)
				return
			}
			entries := append([]Enrollment(nil), s.enrollments...)
			entries[i].Status = update.Status
			entries[i].EnabledQueues = update.EnabledQueues
			entries[i].Message = update.Message
			if update.Usage != nil {
				entries[i].Usage = update.Usage
			}
			entries[i].Updated = time.Now().UTC()
			if s.saveLocked(entries) != nil {
				http.Error(w, "Update could not be saved", 500)
				return
			}
			s.enrollments = entries
			w.WriteHeader(204)
			return
		}
		http.Error(w, "Unknown request", 404)
	})
	return mux
}

func (s *Server) manage(w http.ResponseWriter, r *http.Request) {
	_, v, ok := s.postSession(w, r)
	if !ok {
		return
	}
	action := r.FormValue("action")
	if action != "paused" && action != "active" && action != "disconnected" {
		http.Error(w, "Invalid action", 400)
		return
	}
	choices, e := s.choices(r.Context(), v)
	if e != nil {
		http.Error(w, "GitHub access could not be verified", 502)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, entry := range s.enrollments {
		if entry.ID != r.FormValue("id") || entry.User.ID != v.User.ID {
			continue
		}
		authorized := false
		for _, choice := range choices {
			if choice.Installation.ID == entry.InstallationID && choice.Repository.ID == entry.Repository.ID {
				authorized = true
				break
			}
		}
		if !authorized {
			http.Error(w, "Repository administration access required", 403)
			return
		}
		entries := append([]Enrollment(nil), s.enrollments...)
		entries[i].DesiredState = action
		entries[i].Updated = time.Now().UTC()
		// The reconciler reports applied state; customer requests never claim it.
		if s.saveLocked(entries) != nil {
			http.Error(w, "Request could not be saved", 500)
			return
		}
		s.enrollments = entries
		http.Redirect(w, r, "/dashboard", 303)
		return
	}
	http.Error(w, "Request not authorized", 403)
}
