//go:build darwin

package worker

// Linux ownership fencing is independent of the experimental macOS backend.
func claimHostState(string) error { return nil }
