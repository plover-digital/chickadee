// Experimental per-VM userspace NAT. No host network configuration or listeners.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
	log "github.com/sirupsen/logrus"
)

type filtered struct {
	net.Conn
	deny     []netip.Prefix
	dropped  atomic.Uint64
	accepted atomic.Uint64
	tokens   float64
	last     time.Time
}

func (c *filtered) Read(p []byte) (int, error) {
	packet := make([]byte, 1515)
	for {
		n, err := c.Conn.Read(packet)
		if err != nil {
			return 0, err
		}
		now := time.Now()
		c.tokens += now.Sub(c.last).Seconds() * 5000
		c.last = now
		if c.tokens > 10000 {
			c.tokens = 10000
		}
		if c.tokens < 1 {
			c.dropped.Add(1)
			time.Sleep(time.Millisecond)
			continue
		}
		c.tokens--
		if !allowed(packet[:n], c.deny) {
			c.dropped.Add(1)
			continue
		}
		if n > len(p) {
			c.dropped.Add(1)
			continue
		}
		copy(p, packet[:n])
		c.accepted.Add(1)
		return n, nil
	}
}

func run() error {
	fd := flag.Int("fd", -1, "Inherited connected SOCK_DGRAM descriptor")
	denyFlag := flag.String("deny", "", "Additional IPv4 prefixes; include host public egress address")
	flag.Parse()
	if *fd < 3 || *denyFlag == "" {
		return fmt.Errorf("network descriptor and host deny prefixes required")
	}
	var deny []netip.Prefix
	for _, text := range strings.Split(*denyFlag, ",") {
		p, err := netip.ParsePrefix(text)
		if err != nil || !p.Addr().Is4() {
			return fmt.Errorf("invalid deny prefix")
		}
		deny = append(deny, p)
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			return err
		}
		for _, a := range addresses {
			p, err := netip.ParsePrefix(a.String())
			if err == nil && p.Addr().Is4() {
				deny = append(deny, netip.PrefixFrom(p.Addr(), 32))
			}
		}
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: 512, Max: 512}); err != nil {
		return err
	}
	debug.SetMemoryLimit(192 * 1024 * 1024)
	log.SetLevel(log.ErrorLevel)
	f := os.NewFile(uintptr(*fd), "guest-net")
	conn, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	proxy, err := virtualnetwork.New(&types.Configuration{
		MTU: 1500, Subnet: "192.168.127.0/24", GatewayIP: gatewayIP.String(), DeviceIP: guestIP.String(),
		GatewayMacAddress: "02:cc:aa:00:00:01", DHCPStaticLeases: map[string]string{guestIP.String(): "02:cc:aa:00:00:02"},
		Protocol: types.VfkitProtocol, TCPMaxInFlight: 32, TCPConnectTimeout: 10,
	})
	if err != nil {
		return err
	}
	defer proxy.Close()
	c := &filtered{Conn: conn, deny: deny, tokens: 10000, last: time.Now()}
	go func() { <-ctx.Done(); conn.Close() }()
	fmt.Println("NET_PROXY_READY")
	err = proxy.AcceptVfkit(ctx, c)
	fmt.Printf("NET_PROXY_STOPPED accepted=%d dropped=%d\n", c.accepted.Load(), c.dropped.Load())
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "Network proxy failed")
		os.Exit(1)
	}
}
