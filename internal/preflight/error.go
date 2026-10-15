// Package preflight defines fixed local validation diagnostics. It never renders
// the original error, a configured path, child stderr, or remote response.
package preflight

import "errors"

type failure struct{ code string }

func (e *failure) Error() string { return messages[e.code].problem }

type message struct{ problem, remedy string }

var messages = map[string]message{
	"workspace-permissions":   {"workspace must be service-owned mode 0700", "Precreate every slot workspace as the service user with mode 0700."},
	"workspace-path":          {"workspace must be a direct directory without symlink components", "Precreate the configured workspace directories without symlinks; check every parent directory."},
	"workspace-busy":          {"workspace is already in use", "Drain the existing owner; do not remove live workspace locks or contents."},
	"workspace-not-empty":     {"workspace is not empty; recovery or operator inspection required", "Inspect retained state and finish recovery; do not clear a possibly live workspace."},
	"cgroup-ownership":        {"delegated cgroup root must belong to service user", "Run inside the service user's Delegate=yes unit; the delegated root must belong to that user."},
	"cgroup-root":             {"cgroup root must own this service process or its controller leaf", "Run the check inside the actual delegated service unit; omit a stale cgroup root or correct its configuration."},
	"cgroup-memory-unbounded": {"delegated memory.max must be explicitly bounded", "Set an explicit MemoryMax on the service unit and run the check in that unit."},
	"cgroup-memory-small":     {"delegated memory.max must cover guest budget, per-VM overhead and controller headroom", "Increase the unit MemoryMax or reduce the guest budget and per-runner overhead."},
	"bubblewrap-missing":      {"bubblewrap is required", "Install supported bubblewrap and set the service PATH to select it."},
	"sandbox-probe-failed":    {"Landlock/namespace preflight failed; refusing unsandboxed execution", "Check the service PATH selects supported bubblewrap and that the kernel permits the configured Landlock ABI and namespaces. Child output is withheld."},
}

// New accepts only predefined categories; unknown categories remain generic.
func New(code string) error {
	if _, ok := messages[code]; !ok {
		return errors.New("local preflight failed")
	}
	return &failure{code: code}
}

// Report exposes only fixed trusted strings, including through error wrappers.
func Report(err error) (code, remedy string) {
	var f *failure
	if errors.As(err, &f) {
		if m, ok := messages[f.code]; ok {
			return f.code, m.remedy
		}
	}
	return "config-or-preflight-invalid", "Check private configuration and run -check in the service user's delegation, resource-limit and PATH environment."
}
