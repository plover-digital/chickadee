// Package workerapi defines Chickadee's bounded version-1 worker protocol.
// It contains no QEMU implementation, GitHub credentials or customer policy.
package workerapi

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"time"
)

const (
	Version          = 1
	MaxJIT           = 48 * 1024
	MaxRequestBytes  = 64 * 1024
	MaxResponseBytes = 2 << 20
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var (
	ErrFenced      = errors.New("identity fenced")
	ErrConflict    = errors.New("reservation conflict")
	ErrConsumed    = errors.New("delivery consumed")
	ErrUnavailable = errors.New("worker unavailable")
	ErrNotFound    = errors.New("assignment not found")
)

type Identity struct {
	WorkerID   string `json:"worker_id"`
	BrokerID   string `json:"broker_id"`
	Generation uint64 `json:"generation"`
}

func (i Identity) Valid() bool {
	return idPattern.MatchString(i.WorkerID) && idPattern.MatchString(i.BrokerID) && i.Generation > 0
}

type Budget struct {
	MaxVMs       int `json:"max_vms"`
	MaxCPUs      int `json:"max_cpus"`
	MaxMemoryMiB int `json:"max_memory_mib"`
}
type ProfileInventory struct {
	ID        string `json:"id"`
	Digest    string `json:"digest"`
	Machine   string `json:"machine"`
	CPUs      int    `json:"cpus"`
	MemoryMiB int    `json:"memory_mib"`
	DiskGiB   int    `json:"disk_gib"`
	Ready     int    `json:"ready"`
	Booting   int    `json:"booting"`
}

// SupportedMachine is an additive v1 inventory capability. Older clients reject
// apple-vz inventories, so upgrade the broker before adding native Mac workers.
// Machine and immutable digest both participate in placement identity.
func SupportedMachine(machine string) bool {
	return machine == "q35" || machine == "microvm" || machine == "apple-vz"
}

type Request struct {
	Identity      Identity `json:"identity"`
	AssignmentID  string   `json:"assignment_id"`
	VMID          string   `json:"vm_id"`
	ProfileDigest string   `json:"profile_digest"`
	CPUs          int      `json:"cpus"`
	MemoryMiB     int      `json:"memory_mib"`
	DiskGiB       int      `json:"disk_gib"`
}
type Record struct {
	Resources   *ResourceSummary `json:"resources,omitempty"`
	Request     Request          `json:"request"`
	State       string           `json:"state"`
	CompletedAt time.Time        `json:"completed_at,omitempty"`
}
type CapacityUsed struct {
	VMs       int `json:"vms"`
	CPUs      int `json:"cpus"`
	MemoryMiB int `json:"memory_mib"`
}
type Inventory struct {
	Identity Identity           `json:"identity"`
	Draining bool               `json:"draining"`
	Budget   Budget             `json:"budget"`
	Used     CapacityUsed       `json:"used"`
	Profiles []ProfileInventory `json:"profiles"`
	Records  []Record           `json:"records"`
}
type command struct {
	Version       int      `json:"version"`
	Identity      Identity `json:"identity"`
	AssignmentID  string   `json:"assignment_id,omitempty"`
	ProfileID     string   `json:"profile_id,omitempty"`
	ProfileDigest string   `json:"profile_digest,omitempty"`
	JIT           string   `json:"jit,omitempty"`
}
type response struct {
	Version   int        `json:"version"`
	Inventory *Inventory `json:"inventory,omitempty"`
	Record    *Record    `json:"record,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// Backend is the trusted local engine adapter. No remote terminal/exit-proof API
// exists: only the engine can establish QEMU exit and disk cleanup.
type Backend interface {
	Inventory() (Inventory, error)
	Reserve(context.Context, Identity, string, string, string) (Record, error)
	Seal(Identity, string) (Record, error)
	Deliver(Identity, string, string) error
	Status(Identity, string) (Record, error)
	Drain(Identity) error
}

func (c command) valid(op string, identity Identity) bool {
	if c.Version != Version || c.Identity != identity {
		return false
	}
	switch op {
	case "inventory", "drain":
		return c.AssignmentID == "" && c.ProfileID == "" && c.ProfileDigest == "" && c.JIT == ""
	case "reserve":
		return idPattern.MatchString(c.AssignmentID) && idPattern.MatchString(c.ProfileID) && digestPattern.MatchString(c.ProfileDigest) && c.JIT == ""
	case "seal", "status":
		return idPattern.MatchString(c.AssignmentID) && c.ProfileID == "" && c.ProfileDigest == "" && c.JIT == ""
	case "deliver":
		if !idPattern.MatchString(c.AssignmentID) || c.ProfileID != "" || c.ProfileDigest != "" || len(c.JIT) == 0 || len(c.JIT) > MaxJIT {
			return false
		}
		_, err := base64.StdEncoding.DecodeString(c.JIT)
		return err == nil
	}
	return false
}

func (r Record) valid() bool {
	q := r.Request
	if !r.Resources.Valid() || r.Resources != nil && r.State != "terminal" {
		return false
	}
	if !q.Identity.Valid() || !idPattern.MatchString(q.AssignmentID) || !idPattern.MatchString(q.VMID) || !digestPattern.MatchString(q.ProfileDigest) || q.CPUs < 1 || q.CPUs > 256 || q.MemoryMiB < 512 || q.MemoryMiB > 1<<20 || q.DiskGiB < 8 || q.DiskGiB > 1024 {
		return false
	}
	switch r.State {
	case "reserved", "sealed", "delivery_intent", "terminal":
		return true
	}
	return false
}
func (v Inventory) valid() bool {
	b := v.Budget
	if !v.Identity.Valid() || b.MaxVMs < 1 || b.MaxVMs > 32 || b.MaxCPUs < 1 || b.MaxCPUs > 256 || b.MaxMemoryMiB < 512 || b.MaxMemoryMiB > 1<<20 || v.Used.VMs < 0 || v.Used.VMs > b.MaxVMs || v.Used.CPUs < 0 || v.Used.CPUs > b.MaxCPUs || v.Used.MemoryMiB < 0 || v.Used.MemoryMiB > b.MaxMemoryMiB || len(v.Profiles) == 0 || len(v.Profiles) > 32 || len(v.Records) > 1024 {
		return false
	}
	seen := map[string]bool{}
	for _, p := range v.Profiles {
		if !idPattern.MatchString(p.ID) || seen[p.ID] || !digestPattern.MatchString(p.Digest) || !SupportedMachine(p.Machine) || p.CPUs < 1 || p.CPUs > b.MaxCPUs || p.MemoryMiB < 512 || p.MemoryMiB > b.MaxMemoryMiB || p.DiskGiB < 8 || p.DiskGiB > 1024 || p.Ready < 0 || p.Ready > b.MaxVMs || p.Booting < 0 || p.Booting > b.MaxVMs {
			return false
		}
		seen[p.ID] = true
	}
	for _, r := range v.Records {
		if !r.valid() || r.Request.Identity.WorkerID != v.Identity.WorkerID || r.Request.Identity.Generation > v.Identity.Generation {
			return false
		}
	}
	return true
}
