package host

import (
	"os"
	"path/filepath"
	"sort"
)

// PruneLogs bounds historical logs; current VMs' files are excluded.
func PruneLogs(dir string, active map[string]bool) error {
	entries, e := os.ReadDir(filepath.Join(dir, "logs"))
	if e != nil {
		return e
	}
	type item struct {
		name string
		time int64
	}
	var files []item
	for _, f := range entries {
		if f.IsDir() || len(f.Name()) < 16 || active[f.Name()[:16]] {
			continue
		}
		i, e := f.Info()
		if e != nil {
			return e
		}
		files = append(files, item{f.Name(), i.ModTime().UnixNano()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].time > files[j].time })
	for i := 64; i < len(files); i++ {
		if e = os.Remove(filepath.Join(dir, "logs", files[i].name)); e != nil {
			return e
		}
	}
	return nil
}
