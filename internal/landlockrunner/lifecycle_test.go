//go:build linux && amd64

package landlockrunner

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/plover-digital/chickadee/internal/protocol"
)

func TestTimeoutJoinsProtocolReader(t *testing.T) {
	input, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	m := &Runner{input: input, output: reader, reader: protocol.NewReader(reader)}
	if err = m.Run("e30=", 20*time.Millisecond, filepath.Join(t.TempDir(), "diagnostic")); err == nil {
		t.Fatal("missing timeout")
	}
	if _, err = reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("protocol pipe remained open")
	}
}

func TestDiagnosticRejectsSpecialFilesWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("bounded"), 0600); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	file, err := openDiagnostic(directory, "regular")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	fifo := filepath.Join(dir, "fifo")
	if err = syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink")
	if err = os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{fifo, symlink, dir} {
		if file, err = openDiagnostic(directory, filepath.Base(name)); err == nil {
			file.Close()
			t.Fatalf("accepted nonregular diagnostic %s", name)
		}
	}
}

func TestPartialStartupCleanupClosesPipesAndRemovesState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	m := &Runner{dir: dir, input: writer, output: reader, wait: make(chan struct{}), exited: make(chan struct{})}
	if err = m.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte("secret")); err == nil {
		t.Fatal("input survived cleanup")
	}
	if _, err = reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("output survived cleanup")
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("runtime survived cleanup")
	}
	if err = m.Cleanup(); err != nil {
		t.Fatal("cleanup not idempotent", err)
	}
}
