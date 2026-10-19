package workerapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

// A peer can finish with an empty HTTP 200 response at the same time the caller
// cancels. ReadAll then returns nil even though the request context is canceled.
type cancelAtEOFBody struct {
	cancel context.CancelFunc
	closed bool
}

func (b *cancelAtEOFBody) Read([]byte) (int, error) {
	if b.cancel != nil {
		b.cancel()
	}
	return 0, io.EOF
}
func (b *cancelAtEOFBody) Close() error { b.closed = true; return nil }

type cancellationTransport struct {
	body       *cancelAtEOFBody
	idleClosed bool
}

func (tr *cancellationTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: tr.body}, nil
}
func (tr *cancellationTransport) CloseIdleConnections() { tr.idleClosed = true }

func TestCancellationAtSuccessfulResponseEOF(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "malformed-response"
		if canceled {
			name = "canceled-response"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &cancelAtEOFBody{}
			if canceled {
				body.cancel = cancel
			}
			tr := &cancellationTransport{body: body}
			c := &Client{endpoint: "https://worker.test", identity: Identity{WorkerID: "worker", BrokerID: "broker", Generation: 1}, http: &http.Client{Transport: tr}}
			_, err := c.Inventory(ctx)
			if err == nil || errors.Is(err, context.Canceled) != canceled || errors.Is(err, ErrNotFound) {
				t.Fatalf("canceled=%v: unexpected error classification: %v", canceled, err)
			}
			if !body.closed || tr.idleClosed != canceled {
				t.Fatalf("canceled=%v: body closed=%v, idle cleanup=%v", canceled, body.closed, tr.idleClosed)
			}
		})
	}
}
