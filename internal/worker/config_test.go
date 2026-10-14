//go:build linux && amd64

package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/landlockrunner"
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

func TestConfiguredFreeDiskFloorAndConservativeGrowth(t *testing.T) {
	c := engineConfig(t)
	c.MinFreeDiskGiB = 20
	// Existing allocated blocks never remove the operator free-space floor.
	if err := checkDiskCapacity(c, 1<<40, 19<<30); err == nil {
		t.Fatal("floor bypassed by allocated disks")
	}
	if err := checkDiskCapacity(c, 1<<40, 20<<30); err != nil {
		t.Fatal(err)
	}
	// Returning capacity to a warm pool does not reduce full growth reservation.
	if err := checkDiskCapacity(c, 0, diskReservation(c)-1); err == nil {
		t.Fatal("thin overlay growth underreserved")
	}
	c.MinFreeDiskGiB = -1
	if c.Validate() == nil {
		t.Fatal("negative floor accepted")
	}
	c.MinFreeDiskGiB = 1048577
	if c.Validate() == nil {
		t.Fatal("unbounded floor accepted")
	}
	if checkDiskCapacity(c, -1, 1<<40) == nil {
		t.Fatal("invalid accounting accepted")
	}
}

func TestLandlockModeRequiresResourceAndNetworkPolicyAndDedicatedWorker(t *testing.T) {
	c := engineConfig(t)
	c.Profiles[0].Machine = "landlock"
	if c.Validate() == nil {
		t.Fatal("process runner accepted without mandatory controls")
	}
	c.Cgroup = &host.CgroupConfig{PidsMax: 256}
	c.LandlockDenyPrefixes = []string{"203.0.113.10/32"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	other := c.Profiles[0]
	other.ID = "vm"
	other.Machine = "q35"
	other.Warm = 0
	c.Profiles = append(c.Profiles, other)
	if c.Validate() == nil {
		t.Fatal("shared process/VM worker accepted")
	}
	c.Profiles = c.Profiles[:1]
	c.LandlockDenyPrefixes = []string{"invalid"}
	if c.Validate() == nil {
		t.Fatal("invalid network policy accepted")
	}
}

func TestSharedBundleStillValidatesEachProfileManifest(t *testing.T) {
	for _, machine := range []string{"q35", "microvm"} {
		c := imageFixture(t)
		other := c.Profiles[0]
		other.ID = "other"
		other.Machine = machine
		if machine == "q35" {
			other.DiskGiB++
		}
		c.Profiles = append(c.Profiles, other)
		if err := verifyImages(c); err == nil {
			t.Fatal("shared bundle bypassed per-profile resource/backend validation")
		}
	}
}

func TestConfigCloneOwnsNetworkPolicyAndCgroup(t *testing.T) {
	c := engineConfig(t)
	c.LandlockDenyPrefixes = []string{"8.8.8.8/32"}
	c.Cgroup = &host.CgroupConfig{PidsMax: 256}
	cloned := cloneConfig(c)
	c.LandlockDenyPrefixes[0] = "1.1.1.1/32"
	c.Cgroup.PidsMax = 512
	if cloned.LandlockDenyPrefixes[0] != "8.8.8.8/32" || cloned.Cgroup.PidsMax != 256 {
		t.Fatal("caller mutation changed owned policy")
	}
}

func TestHostAccess32SlotsAndIndependentPolicy(t *testing.T) {
	root, err := os.MkdirTemp("/var/tmp", "chickadee-worker-slots-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	c := engineConfig(t)
	c.Profiles[0].Machine = "landlock"
	c.Profiles[0].CPUs = 1
	c.Profiles[0].MemoryMiB = 512
	c.Profiles[0].Warm = 0
	c.Cgroup = &host.CgroupConfig{PidsMax: 128}
	c.Budget = Budget{MaxVMs: 32, MaxCPUs: 32, MaxMemoryMiB: 32 * 512}
	c.Profiles[0].HostAccess = &landlockrunner.HostAccess{WorkDir: filepath.Join(root, "slot-{slot}"), Env: map[string]string{"PATH": "/usr/bin:/bin"}}
	for i := 1; i <= 32; i++ {
		os.Mkdir(filepath.Join(root, fmt.Sprintf("slot-%d", i)), 0700)
	}
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
	if !c.ResourceCgroup().AllowMissingCPUAndIO {
		t.Fatal("host controller policy not selected")
	}
	copied := cloneConfig(c)
	c.Profiles[0].HostAccess.Env["PATH"] = "changed"
	if copied.Profiles[0].HostAccess.Env["PATH"] != "/usr/bin:/bin" {
		t.Fatal("nested policy was not copied")
	}
	if compatible(c.Profiles[0], copied.Profiles[0]) {
		t.Fatal("different trust/runtime policies share warm executions")
	}
	bad := cloneConfig(c)
	bad.Profiles[0].HostAccess.WorkDir = filepath.Join(root, "slot-1")
	if bad.Validate() == nil {
		t.Fatal("32 executions sharing one directory accepted")
	}
}

func TestLandlockChecksIncludeEveryHostAndSealedProfile(t *testing.T) {
	c := Config{StateDir: "/private/state", Budget: Budget{MaxVMs: 2}, Profiles: []Profile{
		{ID: "first", Machine: "landlock", ImageDir: "/bundle/first", DiskGiB: 8, HostAccess: &landlockrunner.HostAccess{WorkDir: "/shared/first-{slot}"}},
		{ID: "second", Machine: "landlock", ImageDir: "/bundle/second", DiskGiB: 8, HostAccess: &landlockrunner.HostAccess{WorkDir: "/shared/second-{slot}"}},
		{ID: "sealed", Machine: "landlock", ImageDir: "/bundle/sealed", DiskGiB: 8},
	}}
	failed := errors.New("required socket unreachable")
	var visited []string
	probe := func(image string, disk int, access *landlockrunner.HostAccess, slot, max int, protected []string) error {
		visited = append(visited, image)
		if slot != 1 || max != 2 || disk != 8 {
			t.Fatal("wrong probe arguments")
		}
		if image == "/bundle/second" {
			return failed
		}
		return nil
	}
	if err := checkLandlockSandboxes(c, probe); !errors.Is(err, failed) || len(visited) != 2 {
		t.Fatal("second host profile failure ignored", visited, err)
	}
	visited = nil
	err := checkLandlockSandboxes(c, func(image string, _ int, _ *landlockrunner.HostAccess, _ int, _ int, _ []string) error {
		visited = append(visited, image)
		return nil
	})
	if err != nil || len(visited) != 3 {
		t.Fatal("sealed/mixed profile omitted", visited, err)
	}
}

func TestHostWorkspaceCollisionAcrossProfilesAndSlots(t *testing.T) {
	root, err := os.MkdirTemp("/var/tmp", "chickadee-cross-slot-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	c := engineConfig(t)
	c.Cgroup = &host.CgroupConfig{PidsMax: 128}
	c.Profiles[0].Machine = "landlock"
	c.Profiles[0].CPUs = 1
	c.Profiles[0].MemoryMiB = 512
	c.Profiles[0].Warm = 0
	c.Budget = Budget{MaxVMs: 12, MaxCPUs: 12, MaxMemoryMiB: 12 * 512}
	c.Profiles[0].HostAccess = &landlockrunner.HostAccess{WorkDir: filepath.Join(root, "slot-{slot}")}
	other := c.Profiles[0]
	other.ID = "other"
	other.HostAccess = &landlockrunner.HostAccess{WorkDir: filepath.Join(root, "slot-1{slot}")}
	c.Profiles = append(c.Profiles, other)
	for _, p := range c.Profiles {
		for slot := 1; slot <= 12; slot++ {
			work, _ := p.HostAccess.Workspace(slot, 12)
			os.Mkdir(work, 0700)
		}
	}
	if err = c.Validate(); err == nil || !strings.Contains(err.Error(), "across slots") {
		t.Fatal("cross-profile slot11/slot1 collision accepted", err)
	}
	c.Profiles[1].HostAccess = landlockrunner.CloneAccess(c.Profiles[0].HostAccess)
	if err = c.Validate(); err != nil {
		t.Fatal("same scheduler slot aliases rejected", err)
	}
}
