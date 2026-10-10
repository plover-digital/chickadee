//go:build linux && amd64

package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRetentionProtectsActiveAndKeepsNewestCompleted(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	old := "0000000000000001.jsonl"
	newer := "0000000000000002.qemu.log"
	active := "0000000000000003.jsonl"
	for _, name := range []string{old, newer, active} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("1234"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(dir, old), past, past)
	if err := pruneLogs(dir, 8, map[string]bool{"0000000000000001": true, "0000000000000002": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Fatal("old completed retained")
	}
	for _, name := range []string{newer, active} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("protected/newest removed", err)
		}
	}
	if err := pruneLogs(dir, 3, map[string]bool{"0000000000000002": true}); err == nil {
		t.Fatal("protected pressure hidden")
	}
	if _, err := os.Stat(filepath.Join(dir, active)); err != nil {
		t.Fatal("active removed")
	}
}
func TestRetentionNeverFollowsSymlinkOrDeletesUnknown(t *testing.T) {
	for _, unknown := range []string{"unexpected.txt", "0000000000000001.jsonl"} {
		t.Run(unknown, func(t *testing.T) {
			dir := t.TempDir()
			os.Chmod(dir, 0700)
			outside := filepath.Join(t.TempDir(), "outside")
			os.WriteFile(outside, []byte("keep"), 0600)
			if unknown == "unexpected.txt" {
				os.WriteFile(filepath.Join(dir, unknown), []byte("keep"), 0600)
			} else {
				os.Symlink(outside, filepath.Join(dir, unknown))
			}
			if err := pruneLogs(dir, 1, map[string]bool{"0000000000000001": true}); err == nil {
				t.Fatal("unsafe entry accepted")
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
				t.Fatal("outside changed")
			}
			if _, err := os.Lstat(filepath.Join(dir, unknown)); err != nil {
				t.Fatal("unknown removed")
			}
		})
	}
}
