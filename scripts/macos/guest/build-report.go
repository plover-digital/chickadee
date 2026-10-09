// Build-only experimental status transport. Not a runner/JIT protocol.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type header struct {
	V     int    `json:"v"`
	Kind  string `json:"kind"`
	Nonce string `json:"nonce"`
}

func report(status string) error {
	if status != "PROVISIONING" && status != "PROVISIONED_OFFLINE" {
		return fmt.Errorf("invalid build status")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/diskutil", "list", "-plist")
	out, err := cmd.Output()
	if err != nil || len(out) > 256*1024 {
		return fmt.Errorf("disk enumeration failed")
	}
	d := xml.NewDecoder(bytes.NewReader(out))
	whole := false
	var disks []string
	for {
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch x := t.(type) {
		case xml.StartElement:
			if x.Name.Local == "key" {
				var key string
				if d.DecodeElement(&key, &x) != nil {
					return fmt.Errorf("invalid plist")
				}
				whole = key == "WholeDisks"
			}
			if whole && x.Name.Local == "string" {
				var disk string
				if d.DecodeElement(&disk, &x) != nil {
					return fmt.Errorf("invalid disk")
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
		return fmt.Errorf("too many disks")
	}
	valid := regexp.MustCompile(`^disk[0-9]{1,3}$`)
	nonce := regexp.MustCompile(`^[a-f0-9]{32}$`)
	var selected *os.File
	var h header
	defer func() {
		if selected != nil {
			selected.Close()
		}
	}()
	for _, name := range disks {
		if !valid.MatchString(name) {
			return fmt.Errorf("invalid whole disk name")
		}
		f, err := os.OpenFile("/dev/r"+name, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		page := make([]byte, 4096)
		n, err := f.ReadAt(page, 0)
		var candidate header
		decoder := json.NewDecoder(bytes.NewReader(bytes.TrimRight(page, "\x00")))
		decoder.DisallowUnknownFields()
		if err != nil || n != len(page) || decoder.Decode(&candidate) != nil || decoder.Decode(new(any)) != io.EOF || candidate.V != 1 || candidate.Kind != "chickadee-build-control" || !nonce.MatchString(candidate.Nonce) {
			f.Close()
			continue
		}
		if selected != nil {
			f.Close()
			return fmt.Errorf("ambiguous build device")
		}
		selected, h = f, candidate
	}
	if selected == nil {
		return fmt.Errorf("build device absent")
	}
	frame, err := json.Marshal(struct {
		V      int    `json:"v"`
		Nonce  string `json:"nonce"`
		Status string `json:"status"`
	}{1, h.Nonce, strings.TrimSpace(status)})
	if err != nil || len(frame) > 1024 {
		return fmt.Errorf("invalid status frame")
	}
	page := make([]byte, 4096)
	copy(page, frame)
	if _, err = selected.WriteAt(page, 4096); err != nil {
		return err
	}
	// DKIOCSYNCHRONIZECACHE, from the stock macOS sys/disk.h. fsync is not
	// the flush operation for raw character disks.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, selected.Fd(), 0x20006416, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func main() {
	if len(os.Args) != 2 || report(os.Args[1]) != nil {
		fmt.Fprintln(os.Stderr, "Build status report failed")
		os.Exit(1)
	}
}
