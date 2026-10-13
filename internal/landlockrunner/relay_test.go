//go:build linux && amd64

package landlockrunner

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicAddressPolicy(t *testing.T) {
	for _, text := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.2", "192.168.9.184", "169.254.169.254", "100.64.1.2", "198.18.0.1", "203.0.113.1", "224.0.0.1", "::1", "2001:4860:4860::8888", "::ffff:192.168.0.1"} {
		if publicAddress(netip.MustParseAddr(text), nil) {
			t.Fatalf("protected address allowed: %s", text)
		}
	}
	if !publicAddress(netip.MustParseAddr("8.8.8.8"), nil) {
		t.Fatal("public IPv4 rejected")
	}
	if publicAddress(netip.MustParseAddr("8.8.8.8"), mustPrefixes([]string{"8.8.8.8/32"})) {
		t.Fatal("own public egress not denied")
	}
}
func relayClient(t *testing.T, r *relay, c net.Conn) *http.Client {
	t.Helper()
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: p, DialContext: func(context.Context, string, string) (net.Conn, error) { return c, nil }, ResponseHeaderTimeout: 3 * time.Second}
	t.Cleanup(func() { transport.CloseIdleConnections(); r.Close(); c.Close() })
	return &http.Client{Transport: transport}
}
func TestRelayDeniesPrivateWrongPortAndOwnEgressBeforeDial(t *testing.T) {
	var calls atomic.Int32
	for _, target := range []string{"127.0.0.1:443", "169.254.169.254:443", "192.168.9.184:443", "8.8.8.8:443", "1.1.1.1:80", "1.1.1.1:8443", "[::1]:443"} {
		t.Run(target, func(t *testing.T) {
			parent, child := net.Pipe()
			r := newRelayWithDial(parent, mustPrefixes([]string{"8.8.8.8/32"}), func(context.Context, string, string) (net.Conn, error) {
				calls.Add(1)
				return nil, fmt.Errorf("unexpected dial")
			})
			client := relayClient(t, r, child)
			req, _ := http.NewRequest("POST", "http://relay/connect", nil)
			req.Header.Set("X-Target", target)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 403 {
				t.Fatalf("unexpected refusal %d", resp.StatusCode)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("protected destination reached dialer")
	}
}
func TestRelayStreamsValidatedNumericAddressOverPrivateHTTP2(t *testing.T) {
	parent, child := net.Pipe()
	done := make(chan struct{})
	r := newRelayWithDial(parent, nil, func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp4" || addr != "1.1.1.1:443" {
			t.Errorf("unvalidated resolution: %s %s", network, addr)
		}
		local, remote := net.Pipe()
		go func() {
			defer close(done)
			defer remote.Close()
			buf := make([]byte, 4)
			if _, err := io.ReadFull(remote, buf); err == nil {
				remote.Write(buf)
			}
		}()
		return local, nil
	})
	client := relayClient(t, r, child)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	req, _ := http.NewRequest("POST", "http://relay/connect", reader)
	req.Header.Set("X-Target", "1.1.1.1:443")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	go func() { writer.Write([]byte("ping")); writer.Close() }()
	got := make([]byte, 4)
	if _, err = io.ReadFull(resp.Body, got); err != nil || string(got) != "ping" {
		t.Fatalf("full duplex failed: %q %v", got, err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("relay connection leaked")
	}
}
func TestSandboxMountPolicy(t *testing.T) {
	args, err := sandboxArgs("/trusted/bundle", "/trusted/worker", "/private/run", 8, false)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(args, " ")
	for _, required := range []string{"--unshare-user", "--disable-userns", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--cap-drop ALL", "--tmpfs /job", "--size 8589934592", "--ro-bind /trusted/worker /engine"} {
		if !strings.Contains(text, required) {
			t.Fatalf("missing policy %s", required)
		}
	}
	for _, forbidden := range []string{"--bind ", "/dev/kvm", "/dev/net/tun", "docker.sock", "--ro-bind /etc /etc", "--ro-bind /home /home", "--ro-bind /run /run"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("host resource exposed %s", forbidden)
		}
	}
}
