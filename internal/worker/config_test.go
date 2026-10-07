//go:build linux && amd64

package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeylessConfigRejectsManagementFieldsAndOvercommit(t *testing.T) {
	c := engineConfig(t)
	encoded, _ := json.Marshal(c)
	var fields map[string]any
	json.Unmarshal(encoded, &fields)
	fields["app_key_file"] = "not-a-secret"
	encoded, _ = json.Marshal(fields)
	file := filepath.Join(privateDir(t), "config.json")
	os.WriteFile(file, encoded, 0600)
	if _, err := LoadConfig(file); err == nil {
		t.Fatal("GitHub management credential field accepted")
	}
	for _, change := range []func(*Config){func(c *Config) { c.Budget.MaxVMs = 33 }, func(c *Config) { c.Profiles[0].CPUs = 5 }, func(c *Config) { c.Profiles[0].Warm = 2; c.Budget.MaxVMs = 1 }, func(c *Config) { c.ReservationTimeoutSeconds = 181 }, func(c *Config) { c.Profiles[0].ImageDir = c.StateDir + "/images" }} {
		bad := engineConfig(t)
		change(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatal("invalid catalog/resource configuration accepted")
		}
	}
}
func imageFixture(t *testing.T) Config {
	c := engineConfig(t)
	dir := c.Profiles[0].ImageDir
	os.Mkdir(dir, 0700)
	files := map[string]string{"base.qcow2": "immutable-base", "vmlinuz-version": "kernel", "initrd-version": "initrd", "manifest.json": `{"machine":"q35","disk_gib":48}`, "tools.json": "verified-tools"}
	var sums strings.Builder
	for _, name := range []string{"base.qcow2", "vmlinuz-version", "initrd-version", "manifest.json", "tools.json"} {
		data := []byte(files[name])
		os.WriteFile(filepath.Join(dir, name), data, 0600)
		hash := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%x  %s\n", hash, name)
	}
	os.Symlink("vmlinuz-version", filepath.Join(dir, "vmlinuz"))
	os.Symlink("initrd-version", filepath.Join(dir, "initrd"))
	body := []byte(sums.String())
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), body, 0600)
	hash := sha256.Sum256(body)
	c.Profiles[0].Digest = hex.EncodeToString(hash[:])
	return c
}
func TestImageIdentityVerifiesAllFilesAndLocalKernelAliases(t *testing.T) {
	c := imageFixture(t)
	if err := verifyImages(c); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(c.Profiles[0].ImageDir, "tools.json"), []byte("tampered"), 0600)
	if err := verifyImages(c); err == nil {
		t.Fatal("nonexecution checksum tampering ignored")
	}
}
func TestImageAliasEscapeAndDigestMismatchRejected(t *testing.T) {
	c := imageFixture(t)
	c.Profiles[0].Digest = strings.Repeat("b", 64)
	if err := verifyImages(c); err == nil {
		t.Fatal("unpinned image accepted")
	}
	c = imageFixture(t)
	os.Remove(filepath.Join(c.Profiles[0].ImageDir, "vmlinuz"))
	os.Symlink(filepath.Join(privateDir(t), "kernel"), filepath.Join(c.Profiles[0].ImageDir, "vmlinuz"))
	if err := verifyImages(c); err == nil {
		t.Fatal("kernel alias escaping verified bundle accepted")
	}
}

func TestDiskReservationIncludesAllSlotsAndHeadroom(t *testing.T) {
	c := engineConfig(t)
	if got := diskReservation(c); got != 100<<30 {
		t.Fatalf("disk reservation %d want100GiB", got)
	}
	// An impossible trusted capacity config must fail preflight against a bounded capacity fixture.
	c.Budget.MaxVMs = 2
	c.Profiles[0].DiskGiB = 1 << 30
	if err := checkDiskCapacity(c, 0, 14<<30); err == nil {
		t.Fatal("unavailable disk growth was not reserved")
	}
}

func TestAllocatedOverlayBlocksReduceReservationButRetainHeadroom(t *testing.T) {
	c := engineConfig(t)
	if err := checkDiskCapacity(c, 10<<30, 90<<30); err != nil {
		t.Fatal(err)
	}
	if err := checkDiskCapacity(c, 10<<30, 89<<30); err == nil {
		t.Fatal("future disk growth underreserved")
	}
	if err := checkDiskCapacity(c, 1<<50, 1<<30); err == nil {
		t.Fatal("diagnostic headroom omitted")
	}
}

func TestThreeSmallWorkerBudgetAndTapSlotLimit(t *testing.T) {
	c := engineConfig(t)
	c.Budget = Budget{MaxVMs: 3, MaxCPUs: 6, MaxMemoryMiB: 12288}
	c.Profiles[0].Warm = 3
	if err := c.Validate(); err != nil {
		t.Fatalf("three small guests rejected: %v", err)
	}
	c.Profiles[0].Warm = 4
	if err := c.Validate(); err == nil {
		t.Fatal("warm resource overcommit accepted")
	}
	c.Profiles[0].Warm = 0
	c.Budget.MaxVMs = 32
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Budget.MaxVMs = 33
	if err := c.Validate(); err == nil {
		t.Fatal("more TAP slots than available accepted")
	}
}

func TestConfiguredCPUPoolIsOptionalBoundedUniqueAndCoversBudget(t *testing.T) {
	c := engineConfig(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]int{{1, 2, 3}, {1, 1, 2, 3}, {-1, 1, 2, 3}, {0, 1, 2, 8192}} {
		bad := c
		bad.CPUIDs = ids
		if err := bad.Validate(); err == nil {
			t.Fatal("invalid CPU pool accepted")
		}
	}
	c.CPUIDs = []int{30, 10, 50, 20}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
