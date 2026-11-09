//go:build linux && amd64

package landlockrunner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"
)

// A warm sandbox can wait minutes for a JIT before its first HTTPS request.
// The single inherited relay connection must survive that idle period.
func TestRelaySurvivesWarmIdleBeforeFirstCONNECT(t *testing.T) {
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
	r := newRelayWithDial(parent, nil, func(context.Context, string, string) (net.Conn, error) {
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
	time.Sleep(relayIdleProbe)
	local, err := net.DialTimeout("tcp4", "127.0.0.1:3128", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	local.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(local, "CONNECT 1.1.1.1:443 HTTP/1.1\r\nHost: 1.1.1.1:443\r\n\r\n")
	reader := bufio.NewReader(local)
	resp, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("CONNECT after warm idle returned %d", resp.StatusCode)
	}
	local.Write([]byte("ping"))
	got := make([]byte, 4)
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != "ping" {
		t.Fatalf("bridge failed %q %v", got, err)
	}
}

const relayIdleProbe = 7 * time.Second
