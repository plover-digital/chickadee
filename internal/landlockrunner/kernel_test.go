//go:build linux && amd64

package landlockrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/stateowner"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "-landlock-child" {
		probe := len(os.Args) > 2 && os.Args[2] == "-landlock-probe"
		if err := Child(probe); err != nil {
			fmt.Fprintln(os.Stderr, "sandbox child failed")
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestRealHostAccessConcurrentCleanupAndRecovery(t *testing.T) {
	if os.Getenv("CHICKADEE_HOST_ACCESS_SMOKE") != "1" {
		t.Skip("requires isolated delegated unit and verified runner bundle")
	}
	bundle := os.Getenv("CHICKADEE_TEST_RUNNER_BUNDLE")
	if bundle == "" {
		t.Fatal("bundle required")
	}
	cg := &host.CgroupConfig{AllowMissingCPUAndIO: true, PidsMax: 256}
	if err := host.PrepareCgroup(cg); err != nil {
		t.Fatal(err)
	}
	state := hostTemp(t)
	if err := stateowner.State(state); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(state, "vms"), 0700)
	root := hostTemp(t)
	for i := 1; i <= 2; i++ {
		os.Mkdir(filepath.Join(root, fmt.Sprint(i)), 0700)
	}
	toolDir := hostTemp(t)
	if err := os.WriteFile(filepath.Join(toolDir, "public-input"), []byte("tool fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(hostTemp(t), "auth.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	a := &HostAccess{WorkDir: filepath.Join(root, "{slot}"), ROPaths: []string{toolDir}, Sockets: []string{socket}}
	if err := CheckSandboxWithAccess(bundle, 8, a, 1, 2, nil); err != nil {
		t.Fatal("host preflight", err)
	}
	// A killed/failed probe can leave its exact scratch name behind. The parent
	// owns an initially empty, locked directory and removes it after child exit.
	fakeDir := hostTemp(t)
	fake := "#!/bin/sh\nprintf probe > \"" + filepath.Join(root, "1", ".chickadee-probe") + "\"\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(fakeDir, "bwrap"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeDir+":"+originalPath)
	if err := CheckSandboxWithAccess(bundle, 8, a, 1, 2, nil); err == nil {
		t.Fatal("timed-out probe accepted")
	}
	t.Setenv("PATH", originalPath)
	if entries, err := os.ReadDir(filepath.Join(root, "1")); err != nil || len(entries) != 0 {
		t.Fatal("timed-out probe stranded workspace", entries, err)
	}
	c := config.Config{StateDir: state, ImageDir: bundle, Machine: "landlock", CPUs: 1, MemoryMiB: 1024, DiskGiB: 8, Cgroup: cg}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r1, err := StartWithAccess(ctx, c, host.NewID(), nil, a, 1, 2)
	if r1 != nil {
		defer r1.Cleanup()
	}
	if err != nil {
		t.Fatal(err)
	}
	r2, err := StartWithAccess(ctx, c, host.NewID(), nil, a, 2, 2)
	if r2 != nil {
		defer r2.Cleanup()
	}
	if err != nil {
		t.Fatal(err)
	}
	work1, _ := a.Workspace(1, 2)
	work2, _ := a.Workspace(2, 2)
	os.WriteFile(filepath.Join(work1, "job-output"), []byte("one"), 0600)
	os.WriteFile(filepath.Join(work2, "job-output"), []byte("two"), 0600)
	if err = r1.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(work2, "job-output")); err != nil {
		t.Fatal("sibling output removed")
	}
	// Release the dead controller's lock without deleting its marker; recovery
	// must use the original policy/workspace/cgroup identities to reclaim it.
	r2.cmd.Process.Kill()
	<-r2.wait
	if err = r2.confirmExit(); err != nil {
		t.Fatal(err)
	}
	r2.workspace.Close()
	r2.workspace = nil
	plans := []*AccessPlan{r2.plan}
	markerPath := filepath.Join(r2.dir, "sandbox.json")
	original, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	var marker sandboxMarker
	if err = json.Unmarshal(original, &marker); err != nil {
		t.Fatal(err)
	}
	marker.AccessHash = "wrong-policy"
	changed, _ := json.Marshal(marker)
	if err = os.WriteFile(markerPath, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if ReapWithAccess(state, cg, plans) == nil {
		t.Fatal("policy mismatch reclaimed")
	}
	if _, err = os.Stat(filepath.Join(work2, "job-output")); err != nil {
		t.Fatal("mismatch removed output")
	}
	if err = os.WriteFile(markerPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err = ReapWithAccess(state, cg, plans); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(work2, "job-output")); !os.IsNotExist(err) {
		t.Fatal("recovery did not clear owned workspace")
	}
	if _, err = os.Stat(work2); err != nil {
		t.Fatal("operator workspace removed")
	}
	// Reap removed the group/runtime; disable the deferred object's old handle.
	r2.group = nil
	r2.cleaned = true
}

func TestRealSealedSharedMemory(t *testing.T) {
	if os.Getenv("CHICKADEE_HOST_ACCESS_SMOKE") != "1" {
		t.Skip("requires isolated kernel test unit")
	}
	if err := CheckPolicy(); err != nil {
		t.Skip("sealed requires ABI6", err)
	}
	if err := CheckSandbox(os.Getenv("CHICKADEE_TEST_RUNNER_BUNDLE"), 8); err != nil {
		t.Fatal(err)
	}
}

// This models a previous-boot marker without rebooting the test host. An exact
// cgroup in the new boot is unexpected and must not be killed or adopted.
func TestRealPreviousBootRecoveryRequiresAbsentCgroup(t *testing.T) {
	if os.Getenv("CHICKADEE_HOST_ACCESS_SMOKE") != "1" {
		t.Skip("requires isolated delegated unit")
	}
	cg := &host.CgroupConfig{AllowMissingCPUAndIO: true, PidsMax: 128}
	if err := host.PrepareCgroup(cg); err != nil {
		t.Fatal(err)
	}
	state := hostTemp(t)
	if err := stateowner.State(state); err != nil {
		t.Fatal(err)
	}
	id := "1234567890abcdef"
	dir := filepath.Join(state, "vms", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(hostTemp(t), "slot-1")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	plan, err := NormalizeAccess(&HostAccess{WorkDir: work}, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := LockWorkspace(work)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := stateowner.Current()
	if err != nil {
		t.Fatal(err)
	}
	owner.Boot = "11111111-1111-1111-1111-111111111111"
	expected, err := host.ExecutionGroupPath(cg, id)
	if err != nil {
		t.Fatal(err)
	}
	value := sandboxMarker{Version: 3, Backend: "landlock", ID: id, Owner: owner, CgroupPath: expected, AccessHash: plan.Hash, WorkDir: work, Device: workspace.Device, Inode: workspace.Inode}
	workspace.Close()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "sandbox.json")
	if err := os.WriteFile(marker, raw, 0600); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(work, "live")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	group, err := host.NewExecutionGroup(cg, id, 1, 512)
	if err != nil {
		t.Fatal(err)
	}
	defer group.Remove()
	if err := ReapWithAccess(state, cg, []*AccessPlan{plan}); err == nil {
		t.Fatal("previous-boot marker adopted existing cgroup")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "preserve" {
		t.Fatal("refusal changed workspace")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != string(raw) {
		t.Fatal("refusal changed runtime marker")
	}
	if _, err := os.Stat(expected); err != nil {
		t.Fatal("refusal removed cgroup")
	}
	if err := group.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := ReapWithAccess(state, cg, []*AccessPlan{plan}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("previous-boot runtime retained after safe recovery")
	}
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) != 0 {
		t.Fatal("previous-boot workspace not cleared")
	}
}

func TestRealReadOnlyWorkspacePreflightDoesNotClaim(t *testing.T) {
	if os.Getenv("CHICKADEE_HOST_ACCESS_SMOKE") != "1" {
		t.Skip("requires unprivileged namespaces and Landlock")
	}
	parent := hostTemp(t)
	work := filepath.Join(parent, "slot-1")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	access := &HostAccess{WorkDir: work}
	image := hostTemp(t)
	// The probe mounts this path but never extracts or executes a runner bundle.
	if err := os.WriteFile(filepath.Join(image, "runner.tar.gz"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	claim := filepath.Join(parent, ".slot-1.chickadee-owner-v1")
	if err := CheckSandboxWithAccess(image, 8, access, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(claim); !os.IsNotExist(err) {
		t.Fatal("successful real probe claimed unbound workspace")
	}
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) != 0 {
		t.Fatal("successful real probe left scratch data")
	}
	sentinel := filepath.Join(work, "live")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckSandboxWithAccess(image, 8, access, 1, 1, nil); err == nil {
		t.Fatal("dirty workspace passed probe")
	}
	if _, err := os.Lstat(claim); !os.IsNotExist(err) {
		t.Fatal("failed real probe poisoned unbound workspace")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep" {
		t.Fatal("failed real probe changed live data")
	}
}
