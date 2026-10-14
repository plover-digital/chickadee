//go:build linux && amd64

package landlockrunner

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hostTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "chickadee-access-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
func workspaceFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(hostTemp(t), "work")
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestHostAccessValidationAndAliasPlan(t *testing.T) {
	work := workspaceFixture(t)
	tools := filepath.Join(hostTemp(t), "tools")
	os.Mkdir(tools, 0755)
	alias := filepath.Join(hostTemp(t), "alias")
	os.Symlink(tools, alias)
	a := &HostAccess{WorkDir: work, ROPaths: []string{alias}, Env: map[string]string{"PATH": "/usr/bin:/bin"}}
	p, err := NormalizeAccess(a, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Mounts) != 2 || p.WorkDir != work || len(p.Hash) != 64 {
		t.Fatal("incomplete normalized plan", p)
	}
	for _, mutate := range []func(*HostAccess){
		func(a *HostAccess) { a.ROPaths = []string{"/etc"} }, func(a *HostAccess) { a.ROPaths = []string{work} },
		func(a *HostAccess) { a.Env["ACTIONS_RUNNER_INPUT_JITCONFIG"] = "secret" }, func(a *HostAccess) { a.Env["PATH"] = strings.Repeat("x", 8193) },
	} {
		bad := CloneAccess(a)
		mutate(bad)
		if _, err := NormalizeAccess(bad, 1, 1, nil); err == nil {
			t.Fatal("unsafe host plan accepted")
		}
	}
	if _, err = NormalizeAccess(a, 1, 1, []string{tools}); err == nil {
		t.Fatal("canonical protected source accepted")
	}
	if _, err = NormalizeAccess(a, 1, 1, []string{alias}); err == nil {
		t.Fatal("protected path alias bypassed")
	}

	clone := CloneAccess(a)
	a.Env["PATH"] = "changed"
	a.ROPaths[0] = "changed"
	if clone.Env["PATH"] != "/usr/bin:/bin" || clone.ROPaths[0] != alias {
		t.Fatal("caller mutated owned policy")
	}
}
func TestWorkspaceTemplatesAndIdentitySafeContentsCleanup(t *testing.T) {
	work := workspaceFixture(t)
	a := &HostAccess{WorkDir: work}
	if _, err := a.Workspace(1, 2); err == nil {
		t.Fatal("shared workspace allowed for concurrency")
	}
	a.WorkDir = work + "-{slot}"
	seen := map[string]bool{}
	for slot := 1; slot <= 32; slot++ {
		p, err := a.Workspace(slot, 32)
		if err != nil || seen[p] {
			t.Fatal("slot alias", p, err)
		}
		seen[p] = true
	}
	w, err := LockWorkspace(work)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err = LockWorkspace(work); err == nil {
		t.Fatal("double workspace ownership")
	}
	outside := filepath.Join(hostTemp(t), "keep")
	os.WriteFile(outside, []byte("keep"), 0600)
	os.WriteFile(filepath.Join(work, "output"), []byte("job"), 0600)
	os.Symlink(outside, filepath.Join(work, "symlink"))
	if w.Empty() == nil {
		t.Fatal("dirty directory accepted")
	}
	if err = w.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(outside); err != nil {
		t.Fatal("cleanup followed external symlink")
	}
	if _, err = os.Stat(work); err != nil {
		t.Fatal("operator directory removed")
	}
	renamed := work + "-old"
	os.Rename(work, renamed)
	os.Mkdir(work, 0700)
	os.WriteFile(filepath.Join(work, "keep"), nil, 0600)
	if w.Clear() == nil {
		t.Fatal("substitution accepted")
	}
	if _, err = os.Stat(filepath.Join(work, "keep")); err != nil {
		t.Fatal("substitute data deleted")
	}
}
func TestHostSandboxArgsKeepNamespacesAndDropRelay(t *testing.T) {
	work := workspaceFixture(t)
	plan, err := NormalizeAccess(&HostAccess{WorkDir: work}, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	args, err := sandboxArgsWithAccess("/bundle", "/worker", "/runtime", 8, false, plan)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(args, " ")
	if strings.Contains(text, "--unshare-net") || !strings.Contains(text, "--unshare-cgroup") || !strings.Contains(text, "--bind-fd 3 "+work) {
		t.Fatal("incorrect host namespace/mount plan")
	}
	if !strings.Contains(text, "CHICKADEE_HOST_ACCESS") || !strings.Contains(text, "--uid ") {
		t.Fatal("missing child plan")
	}
}

func TestSocketReplacementRetiresWarmExecution(t *testing.T) {
	socket := filepath.Join(hostTemp(t), "auth.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	work := workspaceFixture(t)
	plan, err := NormalizeAccess(&HostAccess{WorkDir: work, Sockets: []string{socket}}, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	file, pin, err := pinMount(plan.Mounts[0])
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	workspace, err := LockWorkspace(work)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	runner := &Runner{plan: plan, pins: []mountIdentity{pin}, workspace: workspace}
	if err = runner.ValidateReady(); err != nil {
		t.Fatal(err)
	}
	listener.Close()
	replacement, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if runner.ValidateReady() == nil {
		t.Fatal("replacement socket retained warm eligibility")
	}
}
func TestPinMountRejectsReplacedParentSymlink(t *testing.T) {
	parent := hostTemp(t)
	source := filepath.Join(parent, "runtime")
	os.Mkdir(source, 0700)
	target := filepath.Join(hostTemp(t), "protected")
	os.Mkdir(target, 0700)
	os.Rename(source, source+"-old")
	os.Symlink(target, source)
	if f, _, err := pinMount(AccessMount{Source: source, Target: source, Kind: "dir"}); err == nil {
		f.Close()
		t.Fatal("source substitution followed")
	}
}

func TestProtectedPathCanonicalizationAndMissingLeaves(t *testing.T) {
	work := workspaceFixture(t)
	parent := hostTemp(t)
	secret := filepath.Join(parent, "private-key")
	if err := os.WriteFile(secret, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(hostTemp(t), "alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, secret)
	if err != nil {
		t.Fatal(err)
	}
	access := &HostAccess{WorkDir: work, ROPaths: []string{parent}}
	for _, protected := range []string{filepath.Join(alias, "private-key"), relative, "/", filepath.Join(alias, "future-state", "journal")} {
		if _, err := NormalizeAccess(access, 1, 1, []string{protected}); err == nil {
			t.Fatal("protected ancestor exposed", protected)
		}
	}
	broken := filepath.Join(hostTemp(t), "broken")
	if err := os.Symlink(filepath.Join(parent, "absent"), broken); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeAccess(&HostAccess{WorkDir: work}, 1, 1, []string{broken}); err == nil {
		t.Fatal("unresolvable protected alias ignored")
	}
}

func TestProtectedSymlinkTraversalPreservesActualPath(t *testing.T) {
	work := workspaceFixture(t)
	parent := hostTemp(t)
	nested := filepath.Join(parent, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(hostTemp(t), "alias")
	if err := os.Symlink(nested, alias); err != nil {
		t.Fatal(err)
	}
	for _, leaf := range []string{"key", "pending/key"} {
		access := &HostAccess{WorkDir: work, ROPaths: []string{parent}}
		// Keep literal /../: joining here would erase the behavior under test.
		if _, err := NormalizeAccess(access, 1, 1, []string{alias + "/../" + leaf}); err == nil {
			t.Fatal("protected traversal exposed", leaf)
		}
	}
}

func TestWorkspaceRejectsLaterReadOnlyRuntimeMounts(t *testing.T) {
	for _, root := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		for _, work := range []string{root, root + "/local/chickadee-work"} {
			_, err := NormalizeAccess(&HostAccess{WorkDir: work}, 1, 1, nil)
			if err == nil || !strings.Contains(err.Error(), "read-only runtime tree") {
				t.Fatal("runtime-shadowed workspace not rejected during validation", work, err)
			}
		}
	}
}

func TestProtectedRuntimePlacementsRejected(t *testing.T) {
	a := &HostAccess{WorkDir: workspaceFixture(t)}
	for _, runtime := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if _, err := NormalizeAccess(a, 1, 1, []string{runtime + "/chickadee-private/key"}); err == nil || !strings.Contains(err.Error(), "built-in runtime") {
			t.Fatal(runtime, err)
		}
	}
	alias := filepath.Join(hostTemp(t), "runtime-alias")
	if err := os.Symlink("/usr", alias); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeAccess(a, 1, 1, []string{alias + "/chickadee-private/key"}); err == nil {
		t.Fatal("canonical runtime key accepted")
	}
}
