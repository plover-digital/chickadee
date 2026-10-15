package github

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/actions/scaleset"
)

type failedSession struct {
	stage   string
	calls   []string
	healthy bool
}

func (s *failedSession) GetMessage(context.Context, int, int) (*scaleset.RunnerScaleSetMessage, error) {
	s.calls = append(s.calls, "get")
	if s.healthy {
		s.healthy = false
		return nil, nil
	}
	if s.stage == "get" {
		return nil, errors.New("private-response-token")
	}
	return &scaleset.RunnerScaleSetMessage{MessageID: 7, JobAvailableMessages: []*scaleset.JobAvailable{{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 1}}}}, nil
}
func (s *failedSession) AcquireJobs(context.Context, []int64) ([]int64, error) {
	s.calls = append(s.calls, "acquire")
	if s.stage == "acquire" {
		return nil, errors.New("private-response-token")
	}
	return nil, nil
}
func (s *failedSession) DeleteMessage(context.Context, int) error {
	s.calls = append(s.calls, "ack")
	return errors.New("private-response-token")
}
func TestListenerCategoriesAndNoReplay(t *testing.T) {
	for _, tc := range []struct {
		stage, code string
		calls       []string
	}{{"get", "listener-get-message-failed", []string{"get"}}, {"acquire", "listener-acquire-failed", []string{"get", "acquire"}}, {"ack", "listener-acknowledgement-failed", []string{"get", "acquire", "ack"}}} {
		t.Run(tc.stage, func(t *testing.T) {
			s := &failedSession{stage: tc.stage}
			err := pollMessages(context.Background(), s, 1, make(chan int, 1))
			if FailureCategory(err) != tc.code || err.Error() != tc.code {
				t.Fatal("unsafe or wrong error", err)
			}
			if !reflect.DeepEqual(s.calls, tc.calls) {
				t.Fatal("ambiguous operation replayed", s.calls)
			}
		})
	}
}
func TestRetryBackoffCapRecoveryAndSafeLogging(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits []time.Duration
	attempt := 0
	err := pollWithWait(ctx, make(chan int, 20), 2*time.Second, func(context.Context, chan<- int) error {
		attempt++
		if attempt == 7 {
			return listenerFailure{category: "listener-get-message-failed", healthy: true}
		}
		return errors.New("private-response-token")
	}, func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		if len(waits) == 8 {
			cancel()
			return ctx.Err()
		}
		return nil
	})
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, 2 * time.Second, 4 * time.Second}
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(waits, want) {
		t.Fatal(err, waits)
	}
	if strings.Contains(logs.String(), "private-response-token") || FailureCategory(errors.New("private-response-token")) != "" {
		t.Fatal("untrusted error exposed")
	}
}
func TestRetryWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	values := make(chan int)
	done := make(chan error, 1)
	go func() {
		done <- PollWithRetry(ctx, values, 30*time.Second, func(context.Context, chan<- int) error { return listenerFailure{category: "listener-session-failed"} })
	}()
	<-values // failure clears outstanding demand before the cancelable reconnect wait.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconnect wait ignored cancellation")
	}
}
func TestHealthyResetRequiresCompletedIteration(t *testing.T) {
	for _, healthy := range []bool{false, true} {
		s := &failedSession{stage: "acquire", healthy: healthy}
		err := pollMessages(context.Background(), s, 1, make(chan int, 1))
		var failure listenerFailure
		if !errors.As(err, &failure) || failure.healthy != healthy {
			t.Fatal("failed acquisition falsely proved recovery", err)
		}
	}
}

func TestSessionCreationCategoryThroughPinnedClient(t *testing.T) {
	api, err := scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{GitHubConfigURL: "https://github.com/example/repository", PersonalAccessToken: "fixture-not-a-token"}, scaleset.WithRetryMax(0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = (&Client{API: api, SetID: 1}).Poll(ctx, "fixture", 1, make(chan int, 1))
	if FailureCategory(err) != "listener-session-failed" || err.Error() != "listener-session-failed" {
		t.Fatal("session error leaked or lost category", err)
	}
}
