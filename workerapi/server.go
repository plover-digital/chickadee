package workerapi

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

type ServerConfig struct {
	Listen   string   `json:"listen"`
	Identity Identity `json:"identity"`
	TLS      TLSFiles `json:"tls"`
}

func NewServer(c ServerConfig, b Backend) (*http.Server, error) {
	if ValidateServerConfig(c) != nil || b == nil {
		return nil, fmt.Errorf("worker requires a fixed private bind and valid identity")
	}
	tls, err := serverTLS(c.TLS)
	if err != nil {
		return nil, err
	}
	return &http.Server{Addr: c.Listen, Handler: NewHandler(c.Identity, b), TLSConfig: tls, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 180 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}, nil
}
func NewHandler(identity Identity, b Backend) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 || !matchesURI(r.TLS.PeerCertificates[0], brokerURI(identity.BrokerID)) {
			write(w, http.StatusForbidden, response{Error: "unauthorized"})
			return
		}
		if r.Method != "POST" || r.URL.RawQuery != "" {
			write(w, http.StatusBadRequest, response{Error: "invalid"})
			return
		}
		op := ""
		switch r.URL.Path {
		case "/v1/inventory":
			op = "inventory"
		case "/v1/reserve":
			op = "reserve"
		case "/v1/seal":
			op = "seal"
		case "/v1/deliver":
			op = "deliver"
		case "/v1/status":
			op = "status"
		case "/v1/drain":
			op = "drain"
		default:
			write(w, http.StatusNotFound, response{Error: "invalid"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
		defer r.Body.Close()
		data, readErr := io.ReadAll(r.Body)
		var req command
		if readErr != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(readErr, &tooLarge) {
				write(w, http.StatusRequestEntityTooLarge, response{Error: "invalid"})
			} else {
				write(w, http.StatusBadRequest, response{Error: "invalid"})
			}
			return
		}
		if strictJSON(data, &req) != nil {
			write(w, http.StatusBadRequest, response{Error: "invalid"})
			return
		}

		if req.Identity != identity {
			write(w, http.StatusConflict, response{Error: "fenced"})
			return
		}
		if !req.valid(op, identity) {
			write(w, http.StatusBadRequest, response{Error: "invalid"})
			return
		}
		var result response
		var err error
		switch op {
		case "inventory":
			var v Inventory
			v, err = b.Inventory()
			result.Inventory = &v
		case "reserve":
			var v Record
			v, err = b.Reserve(r.Context(), identity, req.AssignmentID, req.ProfileID, req.ProfileDigest)
			result.Record = &v
		case "seal":
			var v Record
			v, err = b.Seal(identity, req.AssignmentID)
			result.Record = &v
		case "deliver":
			err = b.Deliver(identity, req.AssignmentID, req.JIT)
		case "status":
			var v Record
			v, err = b.Status(identity, req.AssignmentID)
			result.Record = &v
		case "drain":
			err = b.Drain(identity)
		}
		if err != nil {
			code, status := "unavailable", http.StatusServiceUnavailable
			switch {
			case errors.Is(err, ErrNotFound):
				code, status = "not_found", http.StatusNotFound
			case errors.Is(err, ErrFenced):
				code, status = "fenced", http.StatusConflict
			case errors.Is(err, ErrConsumed):
				code, status = "consumed", http.StatusConflict
			case errors.Is(err, ErrConflict):
				code, status = "conflict", http.StatusConflict
			}
			write(w, status, response{Error: code})
			return
		}
		write(w, http.StatusOK, result)
	})
}
func write(w http.ResponseWriter, status int, result response) {
	result.Version = Version
	data, err := json.Marshal(result)
	if err != nil || len(data) > MaxResponseBytes {
		status = http.StatusServiceUnavailable
		data = []byte(`{"version":1,"error":"unavailable"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// ValidateServerConfig performs no listening or VM initialization.
func ValidateServerConfig(c ServerConfig) error {
	if !c.Identity.Valid() || !privateListen(c.Listen) {
		return fmt.Errorf("worker requires a fixed private bind and valid identity")
	}
	tc, err := serverTLS(c.TLS)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(tc.Certificates[0].Certificate[0])
	if err != nil || !matchesURI(leaf, workerURI(c.Identity.WorkerID)) {
		return fmt.Errorf("server certificate worker identity mismatch")
	}
	return nil
}
