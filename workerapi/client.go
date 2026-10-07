package workerapi

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type ClientConfig struct {
	Endpoint   string   `json:"endpoint"`
	Identity   Identity `json:"identity"`
	ServerName string   `json:"server_name"`
	TLS        TLSFiles `json:"tls"`
}
type Client struct {
	endpoint string
	identity Identity
	http     *http.Client
}
type Error struct {
	Code       string
	HTTPStatus int
}

func (e *Error) Error() string { return fmt.Sprintf("worker request failed (%s)", e.Code) }
func NewClient(c ClientConfig) (*Client, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !c.Identity.Valid() {
		return nil, fmt.Errorf("invalid worker endpoint or identity")
	}
	tls, err := clientTLS(c.TLS, c.ServerName, c.Identity.WorkerID)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(tls.Certificates[0].Certificate[0])
	if err != nil || !matchesURI(leaf, brokerURI(c.Identity.BrokerID)) {
		return nil, fmt.Errorf("client certificate broker identity mismatch")
	}
	tr := &http.Transport{TLSClientConfig: tls, Proxy: nil, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 180 * time.Second}
	return &Client{endpoint: u.Scheme + "://" + u.Host, identity: c.Identity, http: &http.Client{Transport: tr, Timeout: 190 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) call(ctx context.Context, op string, req command) (response, error) {
	req.Version = Version
	req.Identity = c.identity
	if !req.valid(op, c.identity) {
		return response{}, fmt.Errorf("invalid worker request")
	}
	body, _ := json.Marshal(req)
	if len(body) > MaxRequestBytes {
		return response{}, fmt.Errorf("worker request exceeds limit")
	}
	r, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/v1/"+op, bytes.NewReader(body))
	if err != nil {
		return response{}, fmt.Errorf("worker request unavailable")
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			c.http.CloseIdleConnections() // Retire abandoned dials; active RPCs are not interrupted.
			return response{}, fmt.Errorf("worker transport interrupted: %w", ctx.Err())
		}
		return response{}, fmt.Errorf("worker transport unavailable")
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, MaxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil && ctx.Err() != nil {
		c.http.CloseIdleConnections()
		return response{}, fmt.Errorf("worker response interrupted: %w", ctx.Err())
	}
	if err != nil || len(data) > MaxResponseBytes {
		return response{}, fmt.Errorf("worker response exceeds limit")
	}
	var result response
	if strictJSON(data, &result) != nil || result.Version != Version {
		return response{}, fmt.Errorf("invalid worker response")
	}

	if resp.StatusCode != http.StatusOK {
		code := result.Error
		switch code {
		case "unauthorized", "invalid", "fenced", "consumed", "conflict", "unavailable", "not_found":
		default:
			code = "unavailable"
		}
		return response{}, &Error{Code: code, HTTPStatus: resp.StatusCode}
	}
	if result.Error != "" || ((op == "deliver" || op == "drain") && (result.Inventory != nil || result.Record != nil)) {
		return response{}, fmt.Errorf("invalid worker response")
	}
	return result, nil
}
func (c *Client) Inventory(ctx context.Context) (Inventory, error) {
	r, e := c.call(ctx, "inventory", command{})
	if e != nil {
		return Inventory{}, e
	}
	if r.Inventory == nil || r.Record != nil || r.Inventory.Identity != c.identity || !r.Inventory.valid() {
		return Inventory{}, fmt.Errorf("invalid worker inventory")
	}
	return *r.Inventory, nil
}
func (c *Client) record(ctx context.Context, op string, req command) (Record, error) {
	r, e := c.call(ctx, op, req)
	if e != nil {
		return Record{}, e
	}
	if r.Record == nil || r.Inventory != nil || r.Record.Request.Identity != c.identity || r.Record.Request.AssignmentID != req.AssignmentID || !r.Record.valid() || (op == "seal" && r.Record.State != "sealed") {
		return Record{}, fmt.Errorf("invalid worker reservation")
	}
	return *r.Record, nil
}
func (c *Client) Reserve(ctx context.Context, id, profile, digest string) (Record, error) {
	r, e := c.record(ctx, "reserve", command{AssignmentID: id, ProfileID: profile, ProfileDigest: digest})
	if e == nil && r.Request.ProfileDigest != digest {
		return Record{}, fmt.Errorf("worker profile mismatch")
	}
	return r, e
}
func (c *Client) Seal(ctx context.Context, id string) (Record, error) {
	return c.record(ctx, "seal", command{AssignmentID: id})
}
func (c *Client) Deliver(ctx context.Context, id, jit string) error {
	_, e := c.call(ctx, "deliver", command{AssignmentID: id, JIT: jit})
	return e
}
func (c *Client) Status(ctx context.Context, id string) (Record, error) {
	return c.record(ctx, "status", command{AssignmentID: id})
}
func (c *Client) Drain(ctx context.Context) error { _, e := c.call(ctx, "drain", command{}); return e }

func (e *Error) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.Code == "not_found" && e.HTTPStatus == http.StatusNotFound
	case ErrFenced:
		return e.Code == "fenced" && e.HTTPStatus == http.StatusConflict
	case ErrConflict:
		return e.Code == "conflict" && e.HTTPStatus == http.StatusConflict
	case ErrConsumed:
		return e.Code == "consumed" && e.HTTPStatus == http.StatusConflict
	case ErrUnavailable:
		return e.Code == "unavailable"
	}
	return false
}
