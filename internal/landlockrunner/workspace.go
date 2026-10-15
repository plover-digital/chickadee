//go:build linux && amd64

package landlockrunner

import (
	"errors"
	"fmt"
	"github.com/plover-digital/chickadee/internal/preflight"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/plover-digital/chickadee/internal/stateowner"
	"golang.org/x/sys/unix"
)

type Workspace struct {
	Path   string
	Device uint64
	Inode  uint64
	file   *os.File
	root   *os.Root
}

func ValidateWorkspace(path string) error {
	f, err := openWorkspace(path)
	if err != nil {
		return err
	}
	return f.Close()
}
func openWorkspace(path string) (*os.File, error) {
	if !cleanPath(path) {
		return nil, fmt.Errorf("invalid workspace path")
	}
	// Resolve no symlinks in ANY component, atomically, on the supported Linux hosts.
	parent, err := unix.Openat2(unix.AT_FDCWD, filepath.Dir(path), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, preflight.New("workspace-path")
	}
	// The sibling owner claim must share the workspace's storage. Reject bind
	// mounts and filesystem mountpoints rather than leaving the claim host-local.
	fd, err := unix.Openat2(parent, filepath.Base(path), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	unix.Close(parent)
	if errors.Is(err, unix.EXDEV) {
		return nil, fmt.Errorf("workspace slot must not be a filesystem or bind mount; mount storage at its parent")
	}
	if err != nil {
		return nil, preflight.New("workspace-path")
	}
	f := os.NewFile(uintptr(fd), "workspace")
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0700 {
		f.Close()
		return nil, preflight.New("workspace-permissions")
	}
	return f, nil
}
func ClaimWorkspace(path string) error {
	f, err := openWorkspace(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return stateowner.Workspace(path)
}
func LockWorkspace(path string) (*Workspace, error) {
	return lockWorkspace(path, false)
}
func lockWorkspaceForCheck(path string) (*Workspace, error) { return lockWorkspace(path, true) }
func lockWorkspace(path string, checking bool) (*Workspace, error) {
	f, err := openWorkspace(path)
	if err != nil {
		return nil, err
	}
	if checking {
		err = stateowner.CheckWorkspace(path)
	} else {
		err = stateowner.Workspace(path)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, preflight.New("workspace-busy")
	}
	info, _ := f.Stat()
	st := info.Sys().(*syscall.Stat_t)
	// OpenRoot is anchored to the SAME open object, not a second pathname lookup.
	root, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", f.Fd()))
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Workspace{Path: path, Device: uint64(st.Dev), Inode: st.Ino, file: f, root: root}, nil
}
func (w *Workspace) Close() {
	if w == nil {
		return
	}
	if w.root != nil {
		w.root.Close()
		w.root = nil
	}
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
}
func (w *Workspace) Verify() error {
	f, err := openWorkspace(w.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, _ := f.Stat()
	st := info.Sys().(*syscall.Stat_t)
	if uint64(st.Dev) != w.Device || st.Ino != w.Inode {
		return fmt.Errorf("workspace identity changed; retain state")
	}
	return nil
}
func (w *Workspace) Empty() error {
	entries, err := w.root.Open(".")
	if err != nil {
		return err
	}
	defer entries.Close()
	names, err := entries.Readdirnames(1)
	if len(names) > 0 {
		return preflight.New("workspace-not-empty")
	}
	if err != nil && err != io.EOF {
		return err
	}
	return nil
}

// Clear is called only after kernel-confirmed process-tree exit. Root operations
// never follow a job-created symlink outside the pinned directory.
func (w *Workspace) Clear() error {
	if err := w.Verify(); err != nil {
		return err
	}
	dir, err := w.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		names, err := dir.Readdirnames(64)
		for _, name := range names {
			if name == "." || name == ".." || strings.Contains(name, string(filepath.Separator)) {
				return fmt.Errorf("invalid workspace entry")
			}
			if err := w.root.RemoveAll(name); err != nil {
				return err
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}
	if err = w.Verify(); err != nil {
		return err
	}
	return w.Empty()
}
