package nativeworker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/plover-digital/chickadee/internal/worker"
	"github.com/plover-digital/chickadee/workerapi"
)

type session interface {
	Ready() <-chan struct{}
	Done() <-chan struct{}
	Healthy() bool
	Deliver(string, string) error
	Stop()
	Cleaned() bool
}
type startSession func(string) (session, error)
type holder struct {
	id, assignment string
	session        session
	reserved       time.Time
	delivering     bool
}

type Engine struct {
	mu         sync.Mutex
	c          Config
	journal    *worker.Journal
	current    *holder
	start      startSession
	recover    func(*worker.Journal) error
	recovering bool
	draining   bool
	fault      bool
	wake       chan struct{}
	closed     chan struct{}
}

func identity(i workerapi.Identity) worker.Identity {
	return worker.Identity{WorkerID: i.WorkerID, BrokerID: i.BrokerID, Generation: i.Generation}
}
func wire(r worker.Record) workerapi.Record {
	q := r.Request
	return workerapi.Record{Request: workerapi.Request{Identity: workerapi.Identity{WorkerID: q.Identity.WorkerID, BrokerID: q.Identity.BrokerID, Generation: q.Identity.Generation}, AssignmentID: q.AssignmentID, VMID: q.VMID, ProfileDigest: q.ProfileDigest, CPUs: q.CPUs, MemoryMiB: q.MemoryMiB, DiskGiB: q.DiskGiB}, State: string(r.State), CompletedAt: r.CompletedAt, Resources: r.Resources}
}
func translate(e error) error {
	switch {
	case e == nil:
		return nil
	case errors.Is(e, worker.ErrConsumed):
		return workerapi.ErrConsumed
	case errors.Is(e, worker.ErrFenced):
		return workerapi.ErrFenced
	case errors.Is(e, worker.ErrConflict), errors.Is(e, worker.ErrTransition):
		return workerapi.ErrConflict
	case errors.Is(e, worker.ErrNotFound):
		return workerapi.ErrNotFound
	default:
		return workerapi.ErrUnavailable
	}
}

func open(c Config, start startSession, recover func(*worker.Journal) error) (*Engine, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	if e := os.MkdirAll(c.StateDir, 0700); e != nil {
		return nil, e
	}
	if e := ownedPrivate(c.StateDir, true); e != nil {
		return nil, e
	}
	j, e := worker.Open(filepath.Join(c.StateDir, "journal"), identity(c.Identity))
	if e != nil {
		return nil, e
	}
	egn := &Engine{c: c, journal: j, start: start, recover: recover, recovering: recover != nil, wake: make(chan struct{}, 1), closed: make(chan struct{})}
	go egn.loop()
	return egn, nil
}

func (e *Engine) loop() {
	defer close(e.closed)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		e.mu.Lock()
		if e.recovering {
			e.mu.Unlock()
			err := e.recover(e.journal)
			e.mu.Lock()
			if err == nil {
				e.recovering = false
			}
		}
		h := e.current
		if h != nil {
			select {
			case <-h.session.Done():
				if !h.session.Cleaned() {
					e.fault = true
				} else if h.assignment != "" {
					records, err := e.journal.Records()
					if err != nil {
						e.fault = true
					}
					for _, r := range records {
						if r.Request.AssignmentID == h.assignment {
							_, err = e.journal.Terminal(r.Request, worker.ExitProof{VMID: h.id, ProcessExitConfirmed: true, DiskRemoved: true})
							if err != nil {
								e.fault = true
							}
						}
					}
				}
				if !e.fault {
					e.current = nil
				}
			default:
				if h.assignment == "" && e.draining {
					h.session.Stop()
				}
				if h.assignment != "" && !h.delivering && time.Since(h.reserved) > time.Duration(e.c.ReservationTimeoutSeconds)*time.Second {
					h.session.Stop()
				}
			}
		}
		if e.draining && e.current == nil && !e.recovering {
			e.mu.Unlock()
			return
		}
		if e.current == nil && !e.draining && !e.fault && !e.recovering {
			var b [8]byte
			_, err := rand.Read(b[:])
			if err != nil {
				e.fault = true
				e.mu.Unlock()
				continue
			}
			id := "vm-" + hex.EncodeToString(b[:])
			h = &holder{id: id}
			e.current = h
			e.mu.Unlock()
			s, err2 := e.start(id)
			e.mu.Lock()
			if err2 != nil {
				e.fault = true
				e.current = nil
			} else {
				h.session = s
			}
		}
		e.mu.Unlock()
		select {
		case <-tick.C:
		case <-e.wake:
		}
	}
}

func (e *Engine) Inventory() (workerapi.Inventory, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	records, err := e.journal.Records()
	if err != nil {
		return workerapi.Inventory{}, translate(err)
	}
	v := workerapi.Inventory{Identity: e.c.Identity, Draining: e.draining || e.fault || e.recovering, Budget: workerapi.Budget{MaxVMs: 1, MaxCPUs: 2, MaxMemoryMiB: 4096}, Profiles: []workerapi.ProfileInventory{{ID: e.c.ProfileID, Digest: e.c.Digest, Machine: "apple-vz", CPUs: 2, MemoryMiB: 4096, DiskGiB: 64}}}
	for _, r := range records {
		v.Records = append(v.Records, wire(r))
	}
	if h := e.current; h != nil {
		v.Used = workerapi.CapacityUsed{VMs: 1, CPUs: 2, MemoryMiB: 4096}
		if h.assignment == "" {
			if h.session == nil {
				v.Profiles[0].Booting = 1
			} else {
				select {
				case <-h.session.Ready():
					if h.session.Healthy() {
						v.Profiles[0].Ready = 1
					}
				default:
					v.Profiles[0].Booting = 1
				}
			}
		}
	}
	if e.recovering {
		for _, r := range records {
			if r.State != worker.Terminal {
				v.Used = workerapi.CapacityUsed{VMs: 1, CPUs: 2, MemoryMiB: 4096}
				break
			}
		}
	}
	return v, nil
}

func (e *Engine) Reserve(ctx context.Context, i workerapi.Identity, id, profile, digest string) (workerapi.Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i != e.c.Identity {
		return workerapi.Record{}, workerapi.ErrFenced
	}
	if !idPattern.MatchString(id) || profile != e.c.ProfileID || digest != e.c.Digest {
		return workerapi.Record{}, workerapi.ErrConflict
	}
	records, err := e.journal.Records()
	if err != nil {
		return workerapi.Record{}, translate(err)
	}
	for _, r := range records {
		if r.Request.AssignmentID == id {
			return wire(r), nil
		}
	}
	if e.draining || e.fault || e.recovering || ctx.Err() != nil {
		return workerapi.Record{}, workerapi.ErrUnavailable
	}
	h := e.current
	if h == nil || h.session == nil || h.assignment != "" || !h.session.Healthy() {
		return workerapi.Record{}, workerapi.ErrUnavailable
	}
	select {
	case <-h.session.Ready():
	default:
		return workerapi.Record{}, workerapi.ErrUnavailable
	}
	r, err := e.journal.Reserve(worker.Request{Identity: identity(i), AssignmentID: id, VMID: h.id, ProfileDigest: digest, CPUs: 2, MemoryMiB: 4096, DiskGiB: 64})
	if err != nil {
		return workerapi.Record{}, translate(err)
	}
	h.assignment = id
	h.reserved = time.Now()
	return wire(r), nil
}
func (e *Engine) find(i workerapi.Identity, id string) (worker.Record, error) {
	if i != e.c.Identity {
		return worker.Record{}, workerapi.ErrFenced
	}
	records, err := e.journal.Records()
	if err != nil {
		return worker.Record{}, translate(err)
	}
	for _, r := range records {
		if r.Request.AssignmentID == id {
			return r, nil
		}
	}
	return worker.Record{}, workerapi.ErrNotFound
}
func (e *Engine) Seal(i workerapi.Identity, id string) (workerapi.Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.find(i, id)
	if err != nil {
		return workerapi.Record{}, err
	}
	r, err = e.journal.Seal(r.Request)
	return wire(r), translate(err)
}
func (e *Engine) Deliver(i workerapi.Identity, id, jit string) error {
	if len(jit) == 0 || len(jit) > workerapi.MaxJIT {
		return workerapi.ErrConflict
	}
	if _, err := base64.StdEncoding.DecodeString(jit); err != nil {
		return workerapi.ErrConflict
	}
	e.mu.Lock()
	r, err := e.find(i, id)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	h := e.current
	if h == nil || h.assignment != id || h.session == nil {
		e.mu.Unlock()
		return workerapi.ErrUnavailable
	}
	_, err = e.journal.DeliverIntent(r.Request)
	if err != nil {
		e.mu.Unlock()
		return translate(err)
	}
	h.delivering = true
	s := h.session
	e.mu.Unlock()
	if s.Deliver(jit, "chickadee-"+id) != nil {
		s.Stop()
		return workerapi.ErrUnavailable
	}
	return nil
}
func (e *Engine) Status(i workerapi.Identity, id string) (workerapi.Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.find(i, id)
	return wire(r), err
}
func (e *Engine) Drain(i workerapi.Identity) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i != e.c.Identity {
		return workerapi.ErrFenced
	}
	e.draining = true
	select {
	case e.wake <- struct{}{}:
	default:
	}
	return nil
}
func (e *Engine) Wait(ctx context.Context) error {
	select {
	case <-e.closed:
		return e.journal.Close()
	case <-ctx.Done():
		return fmt.Errorf("native worker drain incomplete")
	}
}
