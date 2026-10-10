package nativeworker

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type processSession struct {
	mu                      sync.Mutex
	cmd                     *exec.Cmd
	input                   io.WriteCloser
	ready, done, delivered  chan struct{}
	readyOnce, deliverOnce  sync.Once
	healthy, cleaned, spent bool
}

func (s *processSession) Ready() <-chan struct{} { return s.ready }
func (s *processSession) Done() <-chan struct{}  { return s.done }
func (s *processSession) Healthy() bool          { s.mu.Lock(); defer s.mu.Unlock(); return s.healthy }
func (s *processSession) Cleaned() bool          { s.mu.Lock(); defer s.mu.Unlock(); return s.cleaned }
func (s *processSession) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.healthy {
		s.cmd.Process.Signal(syscall.SIGTERM)
	}
}
func (s *processSession) Deliver(jit, name string) error {
	s.mu.Lock()
	if s.spent || !s.healthy {
		s.mu.Unlock()
		return fmt.Errorf("session spent or unavailable")
	}
	s.spent = true
	data, _ := json.Marshal(map[string]string{"jit": jit, "runner_name": name})
	_, err := s.input.Write(append(data, '\n'))
	s.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case <-s.delivered:
		return nil
	case <-s.done:
		return fmt.Errorf("delivery outcome uncertain")
	case <-time.After(20 * time.Second):
		return fmt.Errorf("delivery acknowledgement timed out")
	}
}

func startProcess(c Config, id string) (session, error) {
	cmd := exec.Command(c.Python, c.Pilot, "--service", "--vm-id", id, "--base", c.BaseDir, "--runtime", filepath.Join(c.StateDir, "vms"), "--native", c.Native, "--netproxy", c.Netproxy, "--deny", strings.Join(c.DenyPrefixes, ","), "--job-timeout", fmt.Sprint(c.JobTimeoutSeconds))
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=en_US.UTF-8"}
	input, e := cmd.StdinPipe()
	if e != nil {
		return nil, e
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		input.Close()
		return nil, e
	}
	cmd.Stderr = io.Discard // Python's controlled failure event is bounded; never log input.
	s := &processSession{cmd: cmd, input: input, ready: make(chan struct{}), done: make(chan struct{}), delivered: make(chan struct{}), healthy: true}
	if e = cmd.Start(); e != nil {
		input.Close()
		return nil, e
	}
	go func() {
		defer close(s.done)
		defer input.Close()
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 1024), 4096)
		for scanner.Scan() {
			var event struct {
				Event string `json:"event"`
			}
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				continue
			}
			switch event.Event {
			case "READY":
				s.readyOnce.Do(func() { close(s.ready) })
			case "CONFIG_DELIVERED":
				s.deliverOnce.Do(func() { close(s.delivered) })
			case "VM_DESTROYED":
				s.mu.Lock()
				s.cleaned = true
				s.mu.Unlock()
			}
		}
		if scanner.Err() != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
		cmd.Wait()
		s.mu.Lock()
		s.healthy = false
		s.mu.Unlock()
	}()
	return s, nil
}
