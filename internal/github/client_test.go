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

func TestMalformedAvailabilityRejectsWholeMessageBeforeSideEffects(t *testing.T) {
	for _, capacity := range []int{0, 1, 3} {
		for _, invalid := range []struct {
			name string
			job  *scaleset.JobAvailable
		}{
			{"null", nil}, {"missing-id", &scaleset.JobAvailable{}},
			{"zero-id", &scaleset.JobAvailable{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 0}}},
			{"negative-id", &scaleset.JobAvailable{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: -1}}},
		} {
			for _, index := range []int{0, 1, 2} {
				t.Run(fmt.Sprintf("capacity-%d/%s-%d", capacity, invalid.name, index), func(t *testing.T) {
					jobs := []*scaleset.JobAvailable{{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 41}}, {JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 42}}, {JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 43}}}
					jobs[index] = invalid.job
					s := &delayedSession{message: &scaleset.RunnerScaleSetMessage{MessageID: 7, Statistics: &scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: 3}, JobAvailableMessages: jobs}, acquiring: make(chan []int64, 1), release: make(chan struct{}), acknowledged: make(chan struct{}, 1)}
					close(s.release)
					desired := make(chan int, 1)
					err := pollMessages(context.Background(), s, capacity, desired)
					if FailureCategory(err) != "listener-message-invalid" || err.Error() != "listener-message-invalid" {
						t.Fatal("malformed message lost bounded category", err)
					}
					if len(s.acquiring) != 0 || len(s.acknowledged) != 0 || len(desired) != 0 {
						t.Fatal("malformed message caused partial acquisition, acknowledgement or demand")
					}
				})
			}
		}
	}
}

func TestAvailableBatchSafetyBoundIsIndependentOfRunnerCapacity(t *testing.T) {
	for _, capacity := range []int{0, 1, maxAvailableJobs} {
		for _, size := range []int{maxAvailableJobs, maxAvailableJobs + 1} {
			t.Run(fmt.Sprintf("capacity-%d/size-%d", capacity, size), func(t *testing.T) {
				jobs := make([]*scaleset.JobAvailable, size)
				for i := range jobs {
					jobs[i] = &scaleset.JobAvailable{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: int64(i + 1)}}
				}
				s := &delayedSession{message: &scaleset.RunnerScaleSetMessage{MessageID: 7, Statistics: &scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: size}, JobAvailableMessages: jobs}, acquiring: make(chan []int64, 1), release: make(chan struct{}), acknowledged: make(chan struct{}, 1)}
				close(s.release)
				desired := make(chan int, 1)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- pollMessages(ctx, s, capacity, desired) }()
				if size > maxAvailableJobs {
					err := <-done
					if FailureCategory(err) != "listener-message-too-large" || err.Error() != "listener-message-too-large" {
						t.Fatal("oversized batch lost fixed error", err)
					}
					if len(s.acquiring) != 0 || len(s.acknowledged) != 0 || len(desired) != 0 {
						t.Fatal("oversized batch caused partial side effects")
					}
					return
				}
				select {
				case ids := <-s.acquiring:
					if len(ids) != size {
						t.Fatal("batch truncated", len(ids))
					}
					for i, id := range ids {
						if id != int64(i+1) {
							t.Fatal("offered request changed or dropped")
						}
					}
				case <-ctx.Done():
					t.Fatal("boundary batch was not acquired")
				}
				select {
				case <-s.acknowledged:
				case <-ctx.Done():
					t.Fatal("boundary batch was not acknowledged")
				}
				if got := <-desired; got != min(capacity, size) {
					t.Fatal("capacity demand semantics changed", got)
				}
				cancel()
				<-done
			})
		}
	}
}
