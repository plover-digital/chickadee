package github

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/actions/scaleset"
)

func TestHostedHomeCacheRestoresToRunnerHome(t *testing.T) {
	// @actions/cache resolvePaths computes this relative archive member on a
	// hosted runner, then tar extracts it relative to our GITHUB_WORKSPACE.
	hostedWorkspace := "/home/runner/work/project/project"
	bun := "/home/runner/.bun/bin/bun"
	member, err := filepath.Rel(hostedWorkspace, bun)
	if err != nil {
		t.Fatal(err)
	}
	guestWorkspace := filepath.Join(runnerWorkFolder, "project", "project")
	if got := filepath.Clean(filepath.Join(guestWorkspace, member)); got != bun {
		t.Fatalf("hosted HOME cache restores to %q instead of %q", got, bun)
	}
}

type delayedSession struct {
	message      *scaleset.RunnerScaleSetMessage
	fetched      bool
	acquiring    chan []int64
	release      chan struct{}
	acquireErr   error
	acknowledged chan struct{}
}

func (s *delayedSession) GetMessage(ctx context.Context, _, _ int) (*scaleset.RunnerScaleSetMessage, error) {
	if !s.fetched {
		s.fetched = true
		return s.message, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s *delayedSession) AcquireJobs(ctx context.Context, ids []int64) ([]int64, error) {
	s.acquiring <- ids
	select {
	case <-s.release:
		return ids, s.acquireErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s *delayedSession) DeleteMessage(context.Context, int) error {
	if s.acknowledged != nil {
		s.acknowledged <- struct{}{}
	}
	return nil
}

func TestAssignedDemandDoesNotWaitForAcquisition(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &delayedSession{message: &scaleset.RunnerScaleSetMessage{Statistics: &scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: 1}, JobAvailableMessages: []*scaleset.JobAvailable{{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 42}}, {JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 43}}}}, acquiring: make(chan []int64, 1), release: make(chan struct{})}
	desired := make(chan int)
	done := make(chan error, 1)
	go func() { done <- pollMessages(ctx, s, 2, desired) }()
	select {
	case n := <-desired:
		if n != 1 {
			t.Fatalf("wanted statistics-derived demand 1, got %d", n)
		}
	case <-time.After(time.Second):
		t.Fatal("assigned demand blocked behind acquisition")
	}
	select {
	case ids := <-s.acquiring:
		if !reflect.DeepEqual(ids, []int64{42, 43}) {
			t.Fatalf("offered requests dropped: %v", ids)
		}
	case <-time.After(time.Second):
		t.Fatal("acquisition never started")
	}
	// The acquisition is still blocked, but the assigned-demand signal arrived.
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation lost")
		}
	case <-time.After(time.Second):
		t.Fatal("poll failed to cancel")
	}
}

func TestAvailabilityDoesNotBecomeAssignedDemand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &delayedSession{message: &scaleset.RunnerScaleSetMessage{Statistics: &scaleset.RunnerScaleSetStatistic{}, JobAvailableMessages: []*scaleset.JobAvailable{{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 42}}}}, acquiring: make(chan []int64, 1), release: make(chan struct{})}
	desired := make(chan int)
	done := make(chan error, 1)
	go func() { done <- pollMessages(ctx, s, 2, desired) }()
	select {
	case n := <-desired:
		if n != 0 {
			t.Fatalf("availability turned into credentials: demand %d", n)
		}
	case <-time.After(time.Second):
		t.Fatal("no statistics signal")
	}
	<-s.acquiring
	close(s.release)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("poll failed to stop")
	}
}

func TestBurstAcquiresAllBeforeAcknowledgementWithOneRunnerSlot(t *testing.T) {
	for _, assigned := range []int{0, 1, 5} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("assigned-%d/failure-%t", assigned, failed), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s := &delayedSession{message: &scaleset.RunnerScaleSetMessage{Statistics: &scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: assigned}}, acquiring: make(chan []int64, 1), release: make(chan struct{}), acknowledged: make(chan struct{}, 1)}
				for _, id := range []int64{41, 42, 43} {
					s.message.JobAvailableMessages = append(s.message.JobAvailableMessages, &scaleset.JobAvailable{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: id}})
				}
				if failed {
					s.acquireErr = errors.New("fixture acquisition failure")
				}
				desired, done := make(chan int, 1), make(chan error, 1)
				go func() { done <- pollMessages(ctx, s, 1, desired) }()
				select {
				case n := <-desired:
					if n != min(1, assigned) {
						t.Fatal("runner demand not capacity-capped", n)
					}
				case <-time.After(time.Second):
					t.Fatal("no demand")
				}
				select {
				case ids := <-s.acquiring:
					if !reflect.DeepEqual(ids, []int64{41, 42, 43}) {
						t.Fatal("burst truncated", ids)
					}
				case <-time.After(time.Second):
					t.Fatal("no acquisition")
				}
				select {
				case <-s.acknowledged:
					t.Fatal("message acknowledged before acquisition")
				default:
				}
				close(s.release)
				if failed {
					select {
					case err := <-done:
						if err == nil {
							t.Fatal("acquisition error lost")
						}
					case <-time.After(time.Second):
						t.Fatal("failure not returned")
					}
					select {
					case <-s.acknowledged:
						t.Fatal("failed acquisition acknowledged")
					default:
					}
				} else {
					select {
					case <-s.acknowledged:
					case <-time.After(time.Second):
						t.Fatal("successful acquisition not acknowledged")
					}
					cancel()
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("poll did not stop")
					}
				}
			})
		}
	}
}
