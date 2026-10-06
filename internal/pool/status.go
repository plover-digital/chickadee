//go:build linux && amd64

package pool

import (
	"encoding/json"
	"github.com/plover-digital/chickadee/internal/config"
	"os"
	"path/filepath"
	"time"
)

type QueueStatus struct {
	Scope     string `json:"github_url"`
	Label     string `json:"label"`
	Requested int    `json:"assigned_demand"`
	Allocated int    `json:"allocated_vms"`
	Ready     int    `json:"ready_vms"`
	Spent     int    `json:"credentialed_vms"`
}
type Status struct {
	Updated  time.Time     `json:"updated_at"`
	Draining bool          `json:"draining"`
	Queues   []QueueStatus `json:"queues"`
}

func writeStatus(c config.Config, configs map[string]config.Config, names []string, entries map[string]*entry, requested map[string]int, draining bool) error {
	status := Status{Updated: time.Now().UTC(), Draining: draining, Queues: []QueueStatus{}}
	for _, name := range names {
		p := configs[name]
		q := QueueStatus{Scope: p.GitHubURL, Label: p.ScaleSet, Requested: requested[name]}
		for _, v := range entries {
			if v.profile != name {
				continue
			}
			q.Allocated++
			if v.state.State == Ready {
				q.Ready++
			}
			if v.state.State == Spent {
				q.Spent++
			}
		}
		status.Queues = append(status.Queues, q)
	}
	b, e := json.Marshal(status)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(c.StateDir, ".status-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), filepath.Join(c.StateDir, "status.json"))
}
