//go:build linux && amd64

package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var logNamePattern = regexp.MustCompile(`^[a-f0-9]{16}\.(jsonl|qemu\.log)$`)

// pruneLogsLocked must run under the engine mutex. Only proven terminal VM
// diagnostics may be removed; active, retiring and unknown files stay untouched.
func (e *Engine) pruneLogsLocked() error {
	if e.config.LogRetentionMiB == 0 {
		return nil
	}
	records, err := e.journal.Records()
	if err != nil {
		return err
	}
	completed := map[string]bool{}
	for _, record := range records {
		if record.State == Terminal {
			completed[record.Request.VMID] = true
		}
	}
	for id := range e.entries {
		delete(completed, id)
	}
	return pruneLogs(filepath.Join(e.config.StateDir, "logs"), int64(e.config.LogRetentionMiB)<<20, completed)
}

type retainedLog struct {
	name string
	info os.FileInfo
}

func pruneLogs(dir string, limit int64, completed map[string]bool) error {
	files, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("diagnostic retention unavailable")
	}
	var total int64
	var removable []retainedLog
	for _, file := range files {
		path := filepath.Join(dir, file.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("diagnostic metadata unavailable")
		}
		if !logNamePattern.MatchString(file.Name()) || !ownedPrivate(info, true) {
			return fmt.Errorf("unrecognized diagnostic entry")
		}
		if info.Size() < 0 || info.Size() > int64(^uint64(0)>>1)-total {
			return fmt.Errorf("invalid diagnostic size")
		}
		total += info.Size()
		id := strings.SplitN(file.Name(), ".", 2)[0]
		if completed[id] {
			removable = append(removable, retainedLog{file.Name(), info})
		}
	}
	sort.Slice(removable, func(i, j int) bool {
		a, b := removable[i], removable[j]
		if !a.info.ModTime().Equal(b.info.ModTime()) {
			return a.info.ModTime().Before(b.info.ModTime())
		}
		return a.name < b.name
	})
	removed := false
	for _, file := range removable {
		if total <= limit {
			break
		}
		path := filepath.Join(dir, file.name)
		current, err := os.Lstat(path)
		if err != nil || !ownedPrivate(current, true) || !os.SameFile(current, file.info) || current.Size() != file.info.Size() {
			return fmt.Errorf("diagnostic changed during retention")
		}
		if os.Remove(path) != nil {
			return fmt.Errorf("diagnostic removal unavailable")
		}
		total -= current.Size()
		removed = true
	}
	if removed {
		f, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	if total > limit {
		return fmt.Errorf("protected diagnostics exceed retention budget")
	}
	return nil
}
