//go:build linux

package worker

import "github.com/plover-digital/chickadee/internal/stateowner"

func claimHostState(dir string) error { return stateowner.State(dir) }
