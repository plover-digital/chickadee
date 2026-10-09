//go:build darwin && arm64

// A root bootstrap for the experimental native macOS one-job pilot.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/plover-digital/chickadee/internal/maccontrol"
)

func findDevice() (*os.File, maccontrol.Header, error) {
	out, err := exec.Command("/usr/sbin/diskutil", "list", "-plist").Output()
	if err != nil || len(out) > 256*1024 {
		return nil, maccontrol.Header{}, fmt.Errorf("disk enumeration failed")
	}
	d := xml.NewDecoder(bytes.NewReader(out))
	whole := false
	var disks []string
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, maccontrol.Header{}, err
		}
		switch x := token.(type) {
		case xml.StartElement:
			if x.Name.Local == "key" {
				var key string
				if d.DecodeElement(&key, &x) != nil {
					return nil, maccontrol.Header{}, fmt.Errorf("invalid plist")
				}
				whole = key == "WholeDisks"
			}
			if whole && x.Name.Local == "string" {
				var disk string
				if d.DecodeElement(&disk, &x) != nil {
					return nil, maccontrol.Header{}, fmt.Errorf("invalid disk")
				}
				disks = append(disks, disk)
			}
		case xml.EndElement:
			if x.Name.Local == "array" {
				whole = false
			}
		}
	}
	if len(disks) > 32 {
		return nil, maccontrol.Header{}, fmt.Errorf("too many disks")
	}
	valid := regexp.MustCompile(`^disk[0-9]{1,3}$`)
	var selected *os.File
	var header maccontrol.Header
	for _, name := range disks {
		if !valid.MatchString(name) {
			continue
		}
		f, err := os.OpenFile("/dev/r"+name, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		page := make([]byte, maccontrol.Page)
		_, err = f.ReadAt(page, 0)
		h, e := maccontrol.ParseHeader(page)
		if err != nil || e != nil {
			f.Close()
			continue
		}
		if selected != nil {
			f.Close()
			selected.Close()
			return nil, header, fmt.Errorf("ambiguous runner control")
		}
		selected, header = f, h
	}
	if selected == nil {
		return nil, header, fmt.Errorf("runner control absent")
	}
	return selected, header, nil
}

func diagnostics() []byte {
	files := []string{"/var/db/chickadee-runner/runner-output.log"}
	entries, _ := filepath.Glob("/Users/runner/actions-runner/_diag/*.log")
	sort.Strings(entries)
	if len(entries) > 4 {
		entries = entries[len(entries)-4:]
	}
	files = append(files, entries...)
	logs := map[string]string{}
	for _, path := range files {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if info.Size() > 65536 {
			f.Seek(info.Size()-65536, 0)
		}
		data, _ := io.ReadAll(io.LimitReader(f, 65536))
		f.Close()
		logs[filepath.Base(path)] = string(data)
	}
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	json.NewEncoder(z).Encode(logs)
	z.Close()
	if b.Len() > maccontrol.MaxLogs {
		return nil
	}
	return b.Bytes()
}

// fsync is not the cache-flush operation for macOS raw character disks.
// DKIOCSYNCHRONIZECACHE = _IO('d', 22), from the stock macOS sys/disk.h.
func flushControl(f *os.File) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), 0x20006416, 0)
	if e != 0 {
		return e
	}
	return nil
}

func bootstrap() error {
	if _, err := os.Stat("/var/db/chickadee-runner/credential-intent"); err == nil {
		return fmt.Errorf("guest already spent")
	}
	f, h, err := findDevice()
	if err != nil {
		return err
	}
	defer f.Close()
	status := func(kind string, sequence, code int, data []byte) error {
		s := maccontrol.Status{V: 1, Nonce: h.Nonce, Type: kind, Sequence: sequence, Code: code}
		if len(data) > 0 {
			padded := make([]byte, ((len(data)+maccontrol.Page-1)/maccontrol.Page)*maccontrol.Page)
			copy(padded, data)
			if _, e := f.WriteAt(padded, maccontrol.LogsOffset); e != nil {
				return e
			}
			hash := sha256.Sum256(data)
			s.LogSize = len(data)
			s.LogSHA256 = hex.EncodeToString(hash[:])
		}
		if e := maccontrol.WriteStatus(f, s); e != nil {
			return e
		}
		return flushControl(f)
	}
	// A headless image built without a NIC may have no configured Ethernet
	// service. Configure DHCP inside the guest, never on the host.
	if h.Network {
		device := ""
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
			interfaces, _ := net.Interfaces()
			for _, iface := range interfaces {
				if iface.HardwareAddr.String() == "02:cc:aa:00:00:02" && regexp.MustCompile(`^en[0-9]{1,2}$`).MatchString(iface.Name) {
					device = iface.Name
					break
				}
			}
			if device != "" {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if device == "" {
			return fmt.Errorf("guest network interface absent")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		exec.CommandContext(ctx, "/usr/sbin/networksetup", "-createnetworkservice", "Chickadee", device).Run()
		e := exec.CommandContext(ctx, "/usr/sbin/networksetup", "-setdhcp", "Chickadee").Run()
		cancel()
		if e != nil {
			return fmt.Errorf("guest DHCP setup failed")
		}
		ready := false
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			out, _ := exec.Command("/usr/sbin/ipconfig", "getifaddr", device).Output()
			if strings.TrimSpace(string(out)) == "192.168.127.2" {
				ready = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !ready {
			return fmt.Errorf("guest DHCP timeout")
		}
	}
	// No registration or management credential exists before this READY message.
	if err = flushControl(f); err != nil {
		return err
	}
	if _, err := os.Stat("/Users/runner/actions-runner/run.sh"); err != nil {
		return err
	}
	if err = status("READY", 0, 0, nil); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Minute)
	var config maccontrol.Config
	for time.Now().Before(deadline) {
		config, err = maccontrol.ReadConfig(f, h.Nonce)
		if err == nil {
			break
		}
		// Curated transport errors only: no configuration or credential bytes.
		page := make([]byte, maccontrol.Page)
		probe := make([]byte, maccontrol.Page)
		n, readErr := f.ReadAt(probe, maccontrol.ConfigOffset)
		var commit maccontrol.Commit
		decoded := maccontrol.Decode(probe, &commit) == nil
		message := map[string]any{"v": 1, "nonce": h.Nonce, "error_code": err.Error(), "commit_bytes": n, "commit_read_ok": readErr == nil, "commit_decoded": decoded, "commit_length": commit.Length, "commit_nonce_matches": commit.Nonce == h.Nonce}
		data, _ := json.Marshal(message)
		copy(page, data)
		f.WriteAt(page, 2*maccontrol.Page)
		flushControl(f)
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return status("EXIT", 3, 1, nil)
	}
	os.MkdirAll("/var/db/chickadee-runner", 0700)
	intent, e := os.OpenFile("/var/db/chickadee-runner/credential-intent", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return fmt.Errorf("guest intent recording failed")
	}
	intent.WriteString("spent\n")
	e = intent.Sync()
	intent.Close()
	if e != nil {
		return e
	}
	if err = status("ACK", 1, 0, nil); err != nil {
		return err
	}
	// Clear the configuration page after acknowledgement. The VM remains spent.
	f.WriteAt(make([]byte, maccontrol.MaxPayload), maccontrol.PayloadOffset)
	f.WriteAt(make([]byte, maccontrol.Page), maccontrol.ConfigOffset)
	flushControl(f)
	os.MkdirAll("/var/db/chickadee-runner", 0700)
	log, err := os.OpenFile("/var/db/chickadee-runner/runner-output.log", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return status("EXIT", 3, 1, nil)
	}
	defer log.Close()
	cmd := exec.Command("/bin/bash", "./run.sh", "--jitconfig", config.JIT)
	cmd.Dir = "/Users/runner/actions-runner"
	cmd.Env = []string{"HOME=/Users/runner", "USER=runner", "LOGNAME=runner", "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=en_US.UTF-8", "DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer", "RUNNER_TOOL_CACHE=/Users/runner/hostedtoolcache"}
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Credential: &syscall.Credential{Uid: 501, Gid: 20, Groups: []uint32{20, 80}}}
	if err = cmd.Start(); err != nil {
		return status("EXIT", 3, 1, nil)
	}
	config.JIT = ""
	if err = status("RUNNER_STARTED", 2, 0, nil); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	code := 0
	select {
	case err = <-done:
		if err != nil {
			code = 1
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
				if code < 0 || code > 255 {
					code = 1
				}
			}
		}
	case <-time.After(10 * time.Minute):
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		code = 124
	}
	log.Sync()
	return status("EXIT", 3, code, diagnostics())
}

func main() {
	if bootstrap() != nil {
		fmt.Fprintln(os.Stderr, "macOS bootstrap failed")
		os.Exit(1)
	}
}
