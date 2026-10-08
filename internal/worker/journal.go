//go:build linux

// Package worker implements a local durable reservation boundary. It does not
// start VMs, authenticate a remote broker, generate JIT, or implement a wire API.
// Callers must enforce actual capacity and authenticate requests before use.
package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/plover-digital/chickadee/workerapi"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

const (
	Version         = 1
	MaxRecords      = 1024
	MaxJournalBytes = 2 << 20
)

var (
	ErrNotFound   = errors.New("assignment not found")
	ErrConflict   = errors.New("reservation identity conflict")
	ErrFenced     = errors.New("worker or broker generation mismatch")
	ErrConsumed   = errors.New("credential delivery intent already committed")
	ErrTransition = errors.New("invalid reservation transition")
	ErrUncertain  = errors.New("journal outcome uncertain; close and reopen before further admission")
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Identity struct {
	WorkerID   string `json:"worker_id"`
	BrokerID   string `json:"broker_id"`
	Generation uint64 `json:"generation"`
}

// Request binds one unique assignment to one unique VM and immutable profile.
// ProfileDigest is a content digest, never a local image path or runner label.
type Request struct {
	Identity      Identity `json:"identity"`
	AssignmentID  string   `json:"assignment_id"`
	VMID          string   `json:"vm_id"`
	ProfileDigest string   `json:"profile_digest"`
	CPUs          int      `json:"cpus"`
	MemoryMiB     int      `json:"memory_mib"`
	DiskGiB       int      `json:"disk_gib"`
}

type State string

const (
	Reserved       State = "reserved"
	Sealed         State = "sealed"
	DeliveryIntent State = "delivery_intent"
	Terminal       State = "terminal"
)

type Record struct {
	Resources   *workerapi.ResourceSummary `json:"resources,omitempty"`
	Request     Request                    `json:"request"`
	State       State                      `json:"state"`
	CompletedAt time.Time                  `json:"completed_at,omitempty"`
}

// ExitProof contains facts the host integration MUST independently establish.
// A broker timeout, guest DONE frame or guardian exit is not process-exit proof.
// This journal cannot verify the process itself and must not be exposed directly
// as an authenticated remote terminal-report endpoint.
type ExitProof struct {
	VMID              string
	QEMUExitConfirmed bool
	Resources         *workerapi.ResourceSummary
	DiskRemoved       bool
}

type snapshot struct {
	Version  int               `json:"version"`
	Identity Identity          `json:"identity"`
	Records  map[string]Record `json:"records"`
}

type Journal struct {
	mu       sync.Mutex
	dir      string
	lock     *os.File
	data     snapshot
	poisoned bool
	// Overridden by package tests to exercise persistence ambiguity.
	persist func(snapshot) error
}

func validIdentity(i Identity) bool {
	return idPattern.MatchString(i.WorkerID) && idPattern.MatchString(i.BrokerID) && i.Generation > 0
}
func validRequest(r Request) bool {
	return validIdentity(r.Identity) && idPattern.MatchString(r.AssignmentID) && idPattern.MatchString(r.VMID) && digestPattern.MatchString(r.ProfileDigest) && r.CPUs > 0 && r.CPUs <= 256 && r.MemoryMiB > 0 && r.MemoryMiB <= 1<<20 && r.DiskGiB > 0 && r.DiskGiB <= 1<<20
}
func ownedPrivate(info os.FileInfo, regular bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0077 == 0 && ((!regular && info.IsDir()) || (regular && info.Mode().IsRegular()))
}
func privateFile(path string, flags int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !ownedPrivate(info, true) {
		file.Close()
		return nil, fmt.Errorf("journal file must be owned, private and regular")
	}
	return file, nil
}

// Open exclusively owns a private local journal. Reopening with a new generation
// fences old requests while preserving their records and consumed-VM tombstones.
// Existing records need explicit local exit recovery, never lease-based reuse.
func Open(dir string, identity Identity) (*Journal, error) {
	if !validIdentity(identity) {
		return nil, ErrFenced
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !ownedPrivate(info, false) {
		return nil, fmt.Errorf("journal directory must be owned, private and not a symlink")
	}
	lock, err := privateFile(filepath.Join(dir, "lock"), syscall.O_CREAT|syscall.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("worker journal already owned")
	}
	j := &Journal{dir: dir, lock: lock, data: snapshot{Version: Version, Identity: identity, Records: map[string]Record{}}}
	j.persist = j.write
	fail := func(err error) (*Journal, error) { lock.Close(); return nil, err }
	file, err := privateFile(filepath.Join(dir, "state.json"), syscall.O_RDONLY)
	if err == nil {
		dec := json.NewDecoder(io.LimitReader(file, MaxJournalBytes+1))
		dec.DisallowUnknownFields()
		err = dec.Decode(&j.data)
		if err == nil && dec.Decode(new(any)) != io.EOF {
			err = fmt.Errorf("invalid journal trailing data")
		}
		info, statErr := file.Stat()
		file.Close()
		if err != nil || statErr != nil || info.Size() > MaxJournalBytes {
			return fail(fmt.Errorf("invalid or oversized worker journal"))
		}
		if err = j.validate(); err != nil {
			return fail(err)
		}
		if j.data.Identity.WorkerID != identity.WorkerID || j.data.Identity.BrokerID != identity.BrokerID {
			return fail(ErrFenced)
		}
		if identity.Generation < j.data.Identity.Generation {
			return fail(ErrFenced)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if j.data.Identity != identity || errors.Is(err, os.ErrNotExist) {
		next := j.clone()
		next.Identity = identity
		if err = j.commit(next); err != nil {
			return fail(err)
		}
	}
	return j, nil
}
func (j *Journal) validate() error {
	if j.data.Version != Version || !validIdentity(j.data.Identity) || j.data.Records == nil || len(j.data.Records) > MaxRecords {
		return fmt.Errorf("invalid worker journal schema")
	}
	vms := map[string]bool{}
	for id, r := range j.data.Records {
		if !validRequest(r.Request) || id != r.Request.AssignmentID || r.Request.Identity.WorkerID != j.data.Identity.WorkerID || r.Request.Identity.BrokerID != j.data.Identity.BrokerID || r.Request.Identity.Generation > j.data.Identity.Generation || vms[r.Request.VMID] {
			return fmt.Errorf("invalid worker reservation identity")
		}
		if !r.Resources.Valid() || r.Resources != nil && r.State != Terminal {
			return fmt.Errorf("invalid resource summary")
		}
		if r.State != Terminal && !r.CompletedAt.IsZero() {
			return fmt.Errorf("completion timestamp before terminal state")
		}
		if !r.CompletedAt.IsZero() && (r.CompletedAt.Unix() <= 0 || r.CompletedAt.After(time.Now().Add(time.Minute))) {
			return fmt.Errorf("invalid completion timestamp")
		}
		if r.State != Reserved && r.State != Sealed && r.State != DeliveryIntent && r.State != Terminal {
			return fmt.Errorf("invalid worker reservation state")
		}
		vms[r.Request.VMID] = true
	}
	return nil
}
func (j *Journal) clone() snapshot {
	next := snapshot{Version: Version, Identity: j.data.Identity, Records: make(map[string]Record, len(j.data.Records))}
	for id, r := range j.data.Records {
		next.Records[id] = r
	}
	return next
}
func (j *Journal) commit(next snapshot) error {
	if err := j.persist(next); err != nil {
		j.poisoned = true
		return fmt.Errorf("%w: persistence failed", ErrUncertain)
	}
	j.data = next
	return nil
}
func (j *Journal) write(next snapshot) error {
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(encoded) > MaxJournalBytes {
		return fmt.Errorf("worker journal budget exceeded")
	}
	file, err := os.CreateTemp(j.dir, ".state-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(encoded); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, filepath.Join(j.dir, "state.json")); err != nil {
		return err
	}
	dir, err := os.Open(j.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (j *Journal) check(r Request) error {
	if j.lock == nil || j.poisoned {
		return ErrUncertain
	}
	if !validRequest(r) {
		return ErrConflict
	}
	if r.Identity != j.data.Identity {
		return ErrFenced
	}
	return nil
}
func (j *Journal) Reserve(request Request) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.check(request); err != nil {
		return Record{}, err
	}
	if old, ok := j.data.Records[request.AssignmentID]; ok {
		if old.Request != request {
			return Record{}, ErrConflict
		}
		// A lost Reserve ACK may reveal a more advanced state, never a fresh VM.
		return old, nil
	}
	if len(j.data.Records) >= MaxRecords {
		return Record{}, fmt.Errorf("worker journal record budget exhausted")
	}
	for _, old := range j.data.Records {
		if old.Request.VMID == request.VMID {
			return Record{}, ErrConflict
		}
	}
	record := Record{Request: request, State: Reserved}
	next := j.clone()
	next.Records[request.AssignmentID] = record
	if err := j.commit(next); err != nil {
		return Record{}, err
	}
	return record, nil
}
func (j *Journal) Seal(request Request) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.check(request); err != nil {
		return Record{}, err
	}
	record, ok := j.data.Records[request.AssignmentID]
	if !ok || record.Request != request {
		return Record{}, ErrConflict
	}
	if record.State == Sealed || record.State == DeliveryIntent {
		return record, nil
	}
	if record.State != Reserved {
		return Record{}, ErrTransition
	}
	record.State = Sealed
	next := j.clone()
	next.Records[request.AssignmentID] = record
	if err := j.commit(next); err != nil {
		return Record{}, err
	}
	return record, nil
}

// DeliverIntent returns success exactly once. Only that successful caller may
// write credentials to serial. An ACK loss, crash or error must never cause a
// second write. No JIT payload enters this API or its persistent journal.
func (j *Journal) DeliverIntent(request Request) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.check(request); err != nil {
		return Record{}, err
	}
	record, ok := j.data.Records[request.AssignmentID]
	if !ok || record.Request != request {
		return Record{}, ErrConflict
	}
	if record.State == DeliveryIntent || record.State == Terminal {
		return record, ErrConsumed
	}
	if record.State != Sealed {
		return Record{}, ErrTransition
	}
	record.State = DeliveryIntent
	next := j.clone()
	next.Records[request.AssignmentID] = record
	if err := j.commit(next); err != nil {
		return Record{}, err
	}
	return record, nil
}
func validProof(request Request, proof ExitProof) bool {
	return proof.VMID == request.VMID && proof.QEMUExitConfirmed && proof.DiskRemoved
}
func (j *Journal) Terminal(request Request, proof ExitProof) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.check(request); err != nil {
		return Record{}, err
	}
	return j.terminal(request, proof)
}

// RecoverTerminal is local host recovery only: old generation assignments are
// fenced from broker operations but can be retired after actual exit+cleanup.
func (j *Journal) RecoverTerminal(assignmentID string, proof ExitProof) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.lock == nil || j.poisoned {
		return Record{}, ErrUncertain
	}
	record, ok := j.data.Records[assignmentID]
	if !ok {
		return Record{}, ErrConflict
	}
	return j.terminal(record.Request, proof)
}
func (j *Journal) terminal(request Request, proof ExitProof) (Record, error) {
	record, ok := j.data.Records[request.AssignmentID]
	if !ok || record.Request != request {
		return Record{}, ErrConflict
	}
	if !validProof(request, proof) {
		return Record{}, ErrTransition
	}
	if record.State == Terminal {
		return record, nil
	}
	if !proof.Resources.Valid() {
		return Record{}, ErrTransition
	}
	record.Resources = proof.Resources
	record.State = Terminal
	record.CompletedAt = time.Now().UTC()
	next := j.clone()
	next.Records[request.AssignmentID] = record
	if err := j.commit(next); err != nil {
		return Record{}, err
	}
	return record, nil
}
func (j *Journal) Records() ([]Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.lock == nil || j.poisoned {
		return nil, ErrUncertain
	}
	records := make([]Record, 0, len(j.data.Records))
	for _, record := range j.data.Records {
		records = append(records, record)
	}
	return records, nil
}
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.lock == nil {
		return nil
	}
	err := j.lock.Close()
	j.lock = nil
	return err
}
