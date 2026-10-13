//go:build linux && amd64

package landlockrunner

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
)

// Only this inherited socket joins the two network namespaces. There is no
// public listener, host socket mount, route, TAP or direct guest networking.
type oneListener struct {
	net.Conn
	once      sync.Once
	done      chan struct{}
	closeOnce sync.Once
}

func (l *oneListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = l.Conn })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *oneListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return l.Conn.Close()
}
func (l *oneListener) Addr() net.Addr { return l.Conn.LocalAddr() }

var blocked = mustPrefixes([]string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/3"})

func mustPrefixes(values []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range values {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}
func publicAddress(ip netip.Addr, denied []netip.Prefix) bool {
	ip = ip.Unmap()
	if !ip.Is4() || !ip.IsGlobalUnicast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	for _, p := range denied {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

type relay struct {
	server   *http.Server
	listener *oneListener
	cancel   context.CancelFunc
	mu       sync.Mutex
	conns    map[net.Conn]bool
}

func newRelay(conn net.Conn, denied []netip.Prefix) *relay {
	return newRelayWithDial(conn, denied, (&net.Dialer{}).DialContext)
}
func newRelayWithDial(conn net.Conn, denied []netip.Prefix, dial func(context.Context, string, string) (net.Conn, error)) *relay {
	ctx, cancel := context.WithCancel(context.Background())
	r := &relay{listener: &oneListener{Conn: conn, done: make(chan struct{})}, cancel: cancel, conns: map[net.Conn]bool{}}
	slots := make(chan struct{}, 32)
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != "POST" || request.URL.Path != "/connect" || request.URL.RawQuery != "" || len(request.Header.Get("X-Target")) > 300 {
			http.Error(w, "invalid", 400)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "busy", 503)
			return
		}
		host, port, err := net.SplitHostPort(request.Header.Get("X-Target"))
		if err != nil || port != "443" || host == "" || strings.ContainsAny(host, "/%\\\x00\r\n") {
			http.Error(w, "denied", 403)
			return
		}
		dialCtx, stop := context.WithTimeout(request.Context(), 10*time.Second)
		defer stop()
		ips, err := net.DefaultResolver.LookupNetIP(dialCtx, "ip4", host)
		if err != nil || len(ips) == 0 || len(ips) > 32 {
			http.Error(w, "denied", 403)
			return
		}
		// Resolve exactly once; reject mixed public/private answers and dial the
		// validated numerical address rather than re-resolving a rebinding hostname.
		for _, ip := range ips {
			if !publicAddress(ip, denied) {
				http.Error(w, "denied", 403)
				return
			}
		}
		dest, err := dial(dialCtx, "tcp4", net.JoinHostPort(ips[0].Unmap().String(), port))
		if err != nil {
			http.Error(w, "unavailable", 502)
			return
		}
		r.mu.Lock()
		if ctx.Err() != nil {
			r.mu.Unlock()
			dest.Close()
			return
		}
		r.conns[dest] = true
		r.mu.Unlock()
		defer func() { dest.Close(); r.mu.Lock(); delete(r.conns, dest); r.mu.Unlock() }()
		go func() {
			select {
			case <-request.Context().Done():
				dest.Close()
			case <-ctx.Done():
				dest.Close()
			}
		}()
		w.WriteHeader(200)
		http.NewResponseController(w).Flush()
		go func() {
			io.Copy(dest, request.Body)
			if tcp, ok := dest.(*net.TCPConn); ok {
				tcp.CloseWrite()
			}
		}()
		// Flush each bounded TCP read so HTTP/2 flow control doesn't wait for EOF.
		buf := make([]byte, 16*1024)
		for {
			n, e := dest.Read(buf)
			if n > 0 {
				if _, err = w.Write(buf[:n]); err != nil {
					return
				}
				if http.NewResponseController(w).Flush() != nil {
					return
				}
			}
			if e != nil {
				return
			}
		}
	})
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	r.server = &http.Server{Handler: handler, Protocols: protocols, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	go r.server.Serve(r.listener)
	return r
}
func (r *relay) Close() {
	r.cancel()
	r.server.Close()
	r.listener.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	for c := range r.conns {
		c.Close()
	}
}

func childProxy(file *os.File) (*http.Server, error) {
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return nil, err
	}
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols, MaxConnsPerHost: 1, ResponseHeaderTimeout: 15 * time.Second, DialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}
	client := &http.Client{Transport: transport}
	slots := make(chan struct{}, 32)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" {
			http.Error(w, "HTTPS CONNECT required", 403)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "busy", 503)
			return
		}
		if len(r.Host) > 300 {
			http.Error(w, "invalid", 400)
			return
		}
		// Keep the TCP tunnel lifetime independent of the HTTP/1 request after hijack.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://relay/connect", reader)
		req.Header.Set("X-Target", r.Host)
		response, err := client.Do(req)
		if err != nil {
			http.Error(w, "unavailable", 502)
			return
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			http.Error(w, "denied", response.StatusCode)
			return
		}
		local, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer local.Close()
		buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		if buffer.Flush() != nil {
			return
		}
		go func() { io.Copy(writer, buffer); writer.Close() }()
		io.Copy(local, response.Body)
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:3128")
	if err != nil {
		conn.Close()
		return nil, err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	go server.Serve(listener)
	// The sandbox process lifetime closes its inherited relay connection. Never
	// export a listener or management socket to the host namespace.
	return server, nil
}
