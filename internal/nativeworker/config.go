package nativeworker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/plover-digital/chickadee/workerapi"
)

type Config struct {
	Version                   int                `json:"version"`
	Identity                  workerapi.Identity `json:"identity"`
	StateDir                  string             `json:"state_dir"`
	BaseDir                   string             `json:"base_dir"`
	ProfileID                 string             `json:"profile_id"`
	Digest                    string             `json:"digest"`
	Python                    string             `json:"python"`
	Pilot                     string             `json:"pilot"`
	Native                    string             `json:"native"`
	Netproxy                  string             `json:"netproxy"`
	DenyPrefixes              []string           `json:"deny_prefixes"`
	NetworkApproved           bool               `json:"network_approved"`
	WarmPool                  int                `json:"warm_pool"`
	JobTimeoutSeconds         int                `json:"job_timeout_seconds"`
	ReservationTimeoutSeconds int                `json:"reservation_timeout_seconds"`
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (c Config) Validate() error {
	if c.Version != 1 || !c.Identity.Valid() || !idPattern.MatchString(c.ProfileID) || !digestPattern.MatchString(c.Digest) || !c.NetworkApproved || c.WarmPool != 1 || c.JobTimeoutSeconds < 30 || c.JobTimeoutSeconds > 600 || c.ReservationTimeoutSeconds < 10 || c.ReservationTimeoutSeconds > 180 {
		return fmt.Errorf("invalid native worker identity, approval or bounds")
	}
	for _, path := range []string{c.StateDir, c.BaseDir, c.Python, c.Pilot, c.Native, c.Netproxy} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return fmt.Errorf("absolute trusted paths required")
		}
	}
	if c.BaseDir == c.StateDir || strings.HasPrefix(c.BaseDir, c.StateDir+"/") || strings.HasPrefix(c.StateDir, c.BaseDir+"/") {
		return fmt.Errorf("base and state directories must be separate")
	}
	if len(c.DenyPrefixes) < 1 || len(c.DenyPrefixes) > 32 {
		return fmt.Errorf("public host egress deny prefix required")
	}
	for _, text := range c.DenyPrefixes {
		p, e := netip.ParsePrefix(text)
		if e != nil || !p.Addr().Is4() {
			return fmt.Errorf("invalid network deny prefix")
		}
	}
	return nil
}

func ownedPrivate(path string, dir bool) error {
	i, e := os.Lstat(path)
	if e != nil {
		return e
	}
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Geteuid()) || i.Mode().Perm()&0077 != 0 || dir && !i.IsDir() || !dir && !i.Mode().IsRegular() {
		return fmt.Errorf("private owned path required")
	}
	return nil
}

// CheckConfig verifies immutable execution bytes once per service startup.
func CheckConfig(c Config) error {
	if e := c.Validate(); e != nil {
		return e
	}
	if e := ownedPrivate(c.BaseDir, true); e != nil {
		return e
	}
	for _, path := range []string{c.Python, c.Pilot, c.Native, c.Netproxy} {
		i, e := os.Stat(path)
		if e != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("trusted helper unavailable or writable by others")
		}
	}
	sums, e := os.ReadFile(filepath.Join(c.BaseDir, "SHA256SUMS"))
	if e != nil || len(sums) > 4096 {
		return fmt.Errorf("base manifest missing")
	}
	digest := sha256.Sum256(sums)
	if hex.EncodeToString(digest[:]) != c.Digest {
		return fmt.Errorf("base digest mismatch")
	}
	expected := map[string]bool{"disk.raw": false, "auxiliary-storage": false, "hardware-model": false}
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 || !digestPattern.MatchString(parts[0]) {
			return fmt.Errorf("invalid checksum manifest")
		}
		name := strings.TrimPrefix(parts[1], "*")
		if _, ok := expected[name]; !ok || expected[name] {
			return fmt.Errorf("unknown or duplicate base input")
		}
		path := filepath.Join(c.BaseDir, name)
		if e := ownedPrivate(path, false); e != nil {
			return e
		}
		i, e := os.Stat(path)
		if e != nil || i.Mode().Perm()&0222 != 0 {
			return fmt.Errorf("base must be read-only")
		}
		if name == "disk.raw" && i.Size() != 64*1024*1024*1024 {
			return fmt.Errorf("native image disk must be 64 GiB")
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		h := sha256.New()
		_, e = io.Copy(h, f)
		f.Close()
		if e != nil || hex.EncodeToString(h.Sum(nil)) != parts[0] {
			return fmt.Errorf("base checksum mismatch")
		}
		expected[name] = true
	}
	for _, seen := range expected {
		if !seen {
			return fmt.Errorf("missing base input")
		}
	}
	return nil
}
