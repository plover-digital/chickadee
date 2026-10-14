//go:build linux && amd64

package landlockrunner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// HostAccess is trusted local configuration, never workflow/JIT input.
// Presence deliberately selects strict V5 and host networking.
type HostAccess struct {
	ROPaths []string          `json:"ro_paths,omitempty"`
	Sockets []string          `json:"sockets,omitempty"`
	WorkDir string            `json:"work_dir"`
	Env     map[string]string `json:"env,omitempty"`
}
type AccessMount struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}
type AccessPlan struct {
	NetworkNamespace string            `json:"network_namespace"`
	Mounts           []AccessMount     `json:"mounts"`
	WorkDir          string            `json:"work_dir"`
	Env              map[string]string `json:"env,omitempty"`
	UID              int               `json:"uid"`
	GID              int               `json:"gid"`
	Hash             string            `json:"hash"`
}

const MaxAccessJSON = 64 * 1024

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func CloneAccess(a *HostAccess) *HostAccess {
	if a == nil {
		return nil
	}
	b := *a
	b.ROPaths = append([]string(nil), a.ROPaths...)
	b.Sockets = append([]string(nil), a.Sockets...)
	b.Env = make(map[string]string, len(a.Env))
	for k, v := range a.Env {
		b.Env[k] = v
	}
	return &b
}
func cleanPath(p string) bool {
	return len(p) <= 4096 && filepath.IsAbs(p) && filepath.Clean(p) == p && p != "/" && !strings.ContainsAny(p, "\x00\r\n")
}
func overlaps(a, b string) bool {
	return a == "/" || b == "/" || a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
func protectedEnv(k string) bool {
	upper := strings.ToUpper(k)
	if strings.HasPrefix(upper, "ACTIONS_RUNNER_INPUT_") || strings.HasPrefix(upper, "CHICKADEE_") {
		return true
	}
	switch upper {
	case "HOME", "TMPDIR", "RUNNER_TOOL_CACHE", "SSL_CERT_FILE", "CURL_CA_BUNDLE", "GIT_SSL_CAINFO", "NODE_EXTRA_CA_CERTS", "RUNNER_ALLOW_RUNASROOT", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
		return true
	}
	return false
}
func (a *HostAccess) Workspace(slot, maxSlots int) (string, error) {
	if a == nil || slot < 1 || slot > maxSlots || maxSlots > 32 {
		return "", fmt.Errorf("invalid host workspace slot")
	}
	count := strings.Count(a.WorkDir, "{slot}")
	if count > 1 || (maxSlots > 1 && count != 1) {
		return "", fmt.Errorf("concurrent host_access requires exactly one {slot} in work_dir")
	}
	p := strings.ReplaceAll(a.WorkDir, "{slot}", fmt.Sprint(slot))
	if !cleanPath(p) || strings.ContainsAny(p, "{}") {
		return "", fmt.Errorf("invalid host workspace path")
	}
	return p, nil
}
func NormalizeAccess(a *HostAccess, slot, maxSlots int, protected []string) (*AccessPlan, error) {
	if a == nil {
		return nil, nil
	}
	if len(a.ROPaths) > 32 || len(a.Sockets) > 16 || len(a.Env) > 128 {
		return nil, fmt.Errorf("host access configuration exceeds bounds")
	}
	work, err := a.Workspace(slot, maxSlots)
	if err != nil {
		return nil, err
	}
	var forbidden []string
	for _, path := range protected {
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("protected host path unavailable")
		}
		rawAbsolute := path
		if !filepath.IsAbs(path) {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("protected host path unavailable")
			}
			rawAbsolute = cwd + "/" + path
		}
		canonical, err := canonicalProtectedPath(rawAbsolute)
		if err != nil {
			return nil, err
		}
		for _, runtime := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
			for _, candidate := range []string{absolute, canonical} {
				if overlaps(candidate, runtime) {
					return nil, fmt.Errorf("protected path overlaps built-in runtime mount")
				}
			}
		}
		forbidden = append(forbidden, absolute, canonical)
	}
	forbidden = append(forbidden, "/job", "/tmp", "/proc", "/sys", "/dev", "/engine", "/image", "/home/runner")
	check := func(p string) error {
		if !cleanPath(p) {
			return fmt.Errorf("host access requires clean absolute paths")
		}
		for _, broad := range []string{"/etc", "/home", "/var", "/run", "/opt"} {
			if p == broad {
				return fmt.Errorf("broad host path is forbidden")
			}
		}
		for _, deny := range forbidden {
			if deny != "" && overlaps(p, deny) {
				return fmt.Errorf("host access overlaps protected paths")
			}
		}
		return nil
	}
	if err = check(work); err != nil {
		return nil, err
	}
	// Parent runtime mounts are installed later and would hide an earlier
	// writable workspace bind. Keep workspace storage outside those trees.
	for _, runtime := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if overlaps(work, runtime) {
			return nil, fmt.Errorf("workspace overlaps read-only runtime tree")
		}
	}
	// Directory ownership/symlink checks are separate from emptiness: recovery
	// must be able to reclaim a matching owned dirty workspace first.
	if err = ValidateWorkspace(work); err != nil {
		return nil, err
	}
	netNS, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		return nil, fmt.Errorf("host network namespace unavailable")
	}
	plan := &AccessPlan{NetworkNamespace: netNS, WorkDir: work, Env: CloneAccess(a).Env, UID: os.Geteuid(), GID: os.Getegid()}
	for k, v := range a.Env {
		if !envName.MatchString(k) || protectedEnv(k) || len(v) > 8192 || strings.ContainsRune(v, 0) {
			return nil, fmt.Errorf("invalid or protected host environment entry")
		}
	}
	add := func(requested, kind string) error {
		if err := check(requested); err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(requested)
		if err != nil {
			return fmt.Errorf("host access source unavailable")
		}
		if err = check(resolved); err != nil {
			return err
		}
		if overlaps(work, requested) || overlaps(work, resolved) {
			return fmt.Errorf("runtime/socket overlaps workspace")
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return err
		}
		if kind == "socket" {
			if info.Mode()&os.ModeSocket == 0 || requested != resolved {
				return fmt.Errorf("socket must be a direct endpoint")
			}
		} else {
			if info.IsDir() {
				kind = "dir"
			} else if info.Mode().IsRegular() {
				kind = "file"
			} else {
				return fmt.Errorf("runtime must be a regular file or directory")
			}
		}
		for _, target := range []string{requested, resolved} {
			duplicate := false
			for _, m := range plan.Mounts {
				if target == m.Target && resolved == m.Source && kind == m.Kind {
					duplicate = true
					continue
				}
				if overlaps(target, m.Target) || (resolved != m.Source && overlaps(resolved, m.Source)) {
					return fmt.Errorf("conflicting host access mounts")
				}
			}
			if !duplicate {
				plan.Mounts = append(plan.Mounts, AccessMount{resolved, target, kind})
			}
		}
		return nil
	}
	for _, p := range a.ROPaths {
		if err = add(p, "ro"); err != nil {
			return nil, err
		}
	}
	for _, p := range a.Sockets {
		if err = add(p, "socket"); err != nil {
			return nil, err
		}
	}
	sort.Slice(plan.Mounts, func(i, j int) bool { return plan.Mounts[i].Target < plan.Mounts[j].Target })
	// Configuration hash deliberately excludes ephemeral slot/source inode data.
	normalized := CloneAccess(a)
	sort.Strings(normalized.ROPaths)
	sort.Strings(normalized.Sockets)
	raw, _ := json.Marshal(struct {
		Version int
		Access  *HostAccess
		Mounts  []AccessMount
		UID     int
		GID     int
	}{1, normalized, plan.Mounts, plan.UID, plan.GID})
	sum := sha256.Sum256(raw)
	plan.Hash = hex.EncodeToString(sum[:])
	raw, _ = json.Marshal(plan)
	if len(raw) > MaxAccessJSON {
		return nil, fmt.Errorf("host access payload exceeds bounds")
	}
	return plan, nil
}
func protectedPaths(state, image string, extra []string) []string {
	return append(append([]string(nil), extra...), state, image)
}

// Resolve existing ancestors even before a state directory or TLS file is created.
// Broken aliases, loops and inaccessible components cannot become an ignored deny.
func canonicalProtectedPath(path string) (string, error) {
	candidate := path
	var missing []string
	for {
		if _, err := os.Lstat(candidate); err == nil {
			canonical, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				return "", fmt.Errorf("protected host path cannot be resolved safely")
			}
			return filepath.Join(append([]string{canonical}, missing...)...), nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("protected host path cannot be resolved safely")
		}
		// Preserve symlink/.. traversal semantics while finding an existing
		// ancestor; filepath.Dir would clean before resolving the symlink.
		candidate = strings.TrimRight(candidate, "/")
		separator := strings.LastIndex(candidate, "/")
		if separator < 0 || candidate == "" {
			return "", fmt.Errorf("protected host path unavailable")
		}
		leaf := candidate[separator+1:]
		if leaf == "." || leaf == ".." {
			return "", fmt.Errorf("unresolved protected traversal")
		}
		missing = append([]string{leaf}, missing...)
		candidate = candidate[:separator]
		if candidate == "" {
			candidate = "/"
		}
	}
}
