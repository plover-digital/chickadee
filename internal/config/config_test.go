package config

import (
	"os"
	"path/filepath"
	"testing"
)

func validConfig(t *testing.T) Config {
	t.Helper()
	c, e := Load("../../examples/config.json")
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestResourceBounds(t *testing.T) {
	for name, change := range map[string]func(*Config){"scope": func(c *Config) { c.GitHubURL = "https://example.com/ORG" }, "overlapping-scope": func(c *Config) { c.GitHubURL = "https://github.com/ORG//REPO" }, "concurrency": func(c *Config) { c.Max = 33 }, "warm": func(c *Config) { c.Warm = c.Max + 1 }, "cpu": func(c *Config) { c.CPUs = 0 }, "memory": func(c *Config) { c.MemoryMiB = 32769 }, "disk": func(c *Config) { c.DiskGiB = 129 }, "boot": func(c *Config) { c.BootSeconds = 0 }, "job": func(c *Config) { c.JobSeconds = 86401 }, "socket-path": func(c *Config) { c.StateDir = "relative" }, "qemu-delimiter": func(c *Config) { c.ImageDir = "/images,cache=unsafe" }} {
		t.Run(name, func(t *testing.T) {
			c := validConfig(t)
			change(&c)
			if c.Validate() == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}
func TestRejectUnknownAndTrailingConfig(t *testing.T) {
	for _, content := range []string{`{"misspelled_pool_size":1}`, `{} {}`} {
		p := filepath.Join(t.TempDir(), "config.json")
		if e := os.WriteFile(p, []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := Load(p); e == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}
