//go:build linux && amd64

package pool

import (
	"fmt"
	"reflect"

	"github.com/plover-digital/chickadee/internal/config"
)

// ProfileUpdate is prepared outside the actor; only the actor publishes it.
// Applied must be buffered so a canceled operator request cannot block the pool.
type ProfileUpdate struct {
	Config   config.Config
	Backends map[string]Backend
	Applied  chan error
}

func sameScopeIdentity(a, b config.Config) bool {
	return config.ScopeKey(a.GitHubURL, "") == config.ScopeKey(b.GitHubURL, "") &&
		a.InstallationID == b.InstallationID && a.RunnerGroupID == b.RunnerGroupID
}

func allScopeProfiles(c config.Config) []config.Config {
	copy := c
	copy.Scopes = make(map[string]config.Scope, len(c.Scopes))
	for name, scope := range c.Scopes {
		scope.Disabled = false
		copy.Scopes[name] = scope
	}
	return copy.ProfileConfigs()
}

// ValidateReload admits queue/scope changes without changing host resources,
// image artifacts, credentials, or the physical/auth identity of existing queues.
// Image/resource changes still require the explicit full drain/restart path.
func ValidateReload(current, next config.Config) error {
	if len(current.Scopes) == 0 || len(next.Scopes) == 0 {
		return fmt.Errorf("live updates require scoped catalogs")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	a, b := current, next
	a.Scopes, b.Scopes = nil, nil
	if !reflect.DeepEqual(a, b) {
		return fmt.Errorf("live update cannot change host limits, images, resources, credentials or lifecycle settings")
	}
	old := allScopeProfiles(current)
	for _, p := range allScopeProfiles(next) {
		for _, previous := range old {
			if config.ScopeKey(previous.GitHubURL, "") == config.ScopeKey(p.GitHubURL, "") && !sameScopeIdentity(previous, p) {
				return fmt.Errorf("live update cannot change an existing scope installation/group")
			}
			if previous.Key() == p.Key() && !compatibleGuest(previous, p) {
				return fmt.Errorf("live update cannot change an existing queue image/resource profile")
			}
		}
	}
	return nil
}
