//go:build linux && amd64

package landlockrunner

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func fixtureArchive(t *testing.T, name, link string, kind byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "runner.tgz")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	g := gzip.NewWriter(f)
	w := tar.NewWriter(g)
	h := &tar.Header{Name: name, Typeflag: kind, Mode: 0755, Linkname: link}
	if kind == tar.TypeReg {
		h.Size = 2
	}
	if e = w.WriteHeader(h); e != nil {
		t.Fatal(e)
	}
	if kind == tar.TypeReg {
		w.Write([]byte("ok"))
	}
	w.Close()
	g.Close()
	f.Close()
	return p
}
func TestExtractRunnerBoundsPathsAndSymlinks(t *testing.T) {
	for _, test := range []struct {
		name, link string
		kind       byte
	}{{"../escape", "", tar.TypeReg}, {"/etc/passwd", "", tar.TypeReg}, {"link", "../../etc/passwd", tar.TypeSymlink}, {"link", "/etc/passwd", tar.TypeSymlink}, {"dev", "", tar.TypeChar}} {
		t.Run(test.name+test.link, func(t *testing.T) {
			if extractRunner(fixtureArchive(t, test.name, test.link, test.kind), filepath.Join(t.TempDir(), "runner")) == nil {
				t.Fatal("unsafe archive entry accepted")
			}
		})
	}
	root := filepath.Join(t.TempDir(), "runner")
	if e := extractRunner(fixtureArchive(t, "bin/runner", "", tar.TypeReg), root); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(root, "bin/runner"))
	if e != nil || string(b) != "ok" {
		t.Fatal("valid archive not extracted")
	}
}
