//go:build linux && amd64

package landlockrunner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestPublicAddressPolicy(t *testing.T) {
	for _, text := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.2", "192.168.42.17", "169.254.169.254", "100.64.1.2", "198.18.0.1", "203.0.113.1", "224.0.0.1", "::1", "2001:4860:4860::8888", "::ffff:192.168.0.1"} {
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
	for _, target := range []string{"127.0.0.1:443", "169.254.169.254:443", "192.168.42.17:443", "8.8.8.8:443", "1.1.1.1:80", "1.1.1.1:8443", "[::1]:443"} {
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

func TestRelayRefreshesHostAddressesAfterWarmStartup(t *testing.T) {
	parent, child := net.Pipe()
	var changed atomic.Bool
	var calls atomic.Int32
	r := newRelayWithInterfaces(parent, nil, func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, fmt.Errorf("no actual dial")
	}, func() ([]net.Addr, error) {
		if changed.Load() {
			return []net.Addr{&net.IPNet{IP: net.ParseIP("1.1.1.1"), Mask: net.CIDRMask(24, 32)}}, nil
		}
		return nil, nil
	})
	client := relayClient(t, r, child)
	for _, want := range []int{502, 403} {
		req, _ := http.NewRequest("POST", "http://relay/connect", nil)
		req.Header.Set("X-Target", "1.1.1.1:443")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("status %d want %d", resp.StatusCode, want)
		}
		changed.Store(true)
	}
	if calls.Load() != 1 {
		t.Fatal("new host address reached dialer")
	}
}

func TestCONNECTProxySocketBridgeAndDenial(t *testing.T) {
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parentFile := os.NewFile(uintptr(pair[0]), "relay")
	parent, err := net.FileConn(parentFile)
	parentFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	r := newRelayWithDial(parent, nil, func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		local, remote := net.Pipe()
		go func() {
			defer remote.Close()
			buf := make([]byte, 4)
			if _, err := io.ReadFull(remote, buf); err == nil {
				remote.Write(buf)
			}
		}()
		return local, nil
	})
	defer r.Close()
	p, err := childProxy(os.NewFile(uintptr(pair[1]), "child-relay"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, target := range []string{"1.1.1.1:443", "169.254.169.254:443"} {
		local, err := net.DialTimeout("tcp4", "127.0.0.1:3128", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		local.SetDeadline(time.Now().Add(3 * time.Second))
		fmt.Fprintf(local, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		reader := bufio.NewReader(local)
		resp, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
		if err != nil {
			local.Close()
			t.Fatal(err)
		}
		if target == "1.1.1.1:443" {
			if resp.StatusCode != 200 {
				t.Fatal("CONNECT rejected", resp.StatusCode)
			}
			local.Write([]byte("ping"))
			got := make([]byte, 4)
			if _, err := io.ReadFull(reader, got); err != nil || string(got) != "ping" {
				t.Fatalf("bridge failed %q %v", got, err)
			}
		} else if resp.StatusCode != 403 {
			t.Fatal("denial did not propagate", resp.StatusCode)
		}
		local.Close()
	}
	if calls.Load() != 1 {
		t.Fatal("protected CONNECT reached dialer")
	}
}

func TestRuntimeAliasesNeverExposeOtherHostTrees(t *testing.T) {
	for _, path := range []string{"/etc/shadow", "/home/key", "/var/lib/private", "/usr-secret/key", "/run/socket"} {
		if publicRuntimeTarget(path) {
			t.Fatal("nonruntime alias exposed", path)
		}
	}
	for _, path := range []string{"/usr/bin/ld.bfd", "/usr/lib/library.so", "/lib64/loader.so"} {
		if !publicRuntimeTarget(path) {
			t.Fatal("public runtime alias rejected", path)
		}
	}
}
