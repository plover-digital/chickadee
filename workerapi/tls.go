package workerapi

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"
)

type TLSFiles struct {
	CertificateFile string `json:"certificate_file"`
	KeyFile         string `json:"key_file"`
	CAFile          string `json:"ca_file"`
}

func brokerURI(id string) string { return "spiffe://chickadee/broker/" + id }
func workerURI(id string) string { return "spiffe://chickadee/worker/" + id }
func loadTLS(f TLSFiles) (tls.Certificate, *x509.CertPool, error) {
	info, err := os.Lstat(f.KeyFile)
	if err != nil || !info.Mode().IsRegular() || (info.Mode().Perm() != 0600 && info.Mode().Perm() != 0640 && info.Mode().Perm() != 0400 && info.Mode().Perm() != 0440) {
		return tls.Certificate{}, nil, fmt.Errorf("TLS key must be a private regular file")
	}
	cert, err := tls.LoadX509KeyPair(f.CertificateFile, f.KeyFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("TLS key pair unavailable")
	}
	pem, err := os.ReadFile(f.CAFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("pinned CA unavailable")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return tls.Certificate{}, nil, fmt.Errorf("pinned CA invalid")
	}
	return cert, pool, nil
}
func matchesURI(cert *x509.Certificate, want string) bool {
	return cert != nil && len(cert.URIs) == 1 && cert.URIs[0].String() == want
}
func privateListen(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && !ip.IsUnspecified() && (ip.IsPrivate() || ip.IsLoopback())
}
func serverTLS(files TLSFiles) (*tls.Config, error) {
	cert, pool, err := loadTLS(files)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}, nil
}
func clientTLS(files TLSFiles, serverName, workerID string) (*tls.Config, error) {
	cert, pool, err := loadTLS(files)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(serverName) == "" {
		return nil, fmt.Errorf("worker TLS server name required")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: pool, ServerName: serverName, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 || !matchesURI(cs.PeerCertificates[0], workerURI(workerID)) {
			return fmt.Errorf("worker certificate identity mismatch")
		}
		return nil
	}}, nil
}
