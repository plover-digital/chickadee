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

func catalog(t *testing.T) Config {
	c := validConfig(t)
	c.ScaleSet = ""
	c.Warm = 0
	c.Max = 0
	c.CPUs = 0
	c.MemoryMiB = 0
	c.DiskGiB = 0
	c.ImageDir = ""
	c.Images = map[string]Image{"ubuntu-2404": {Machine: "microvm", Path: "/var/lib/chickadee-images/ubuntu-2404/v1", DiskGiB: 16, OS: "ubuntu", Version: "24.04"}}
	c.ResourceClasses = map[string]Resources{"small": {CPUs: 2, MemoryMiB: 4096}, "medium": {CPUs: 4, MemoryMiB: 8192}}
	c.Profiles = map[string]Profile{"chickadee": {Image: "ubuntu-2404", Resources: "medium", Warm: 1, Max: 1}, "chickadee-small-ubuntu-2404": {Image: "ubuntu-2404", Resources: "small", Max: 2}}
	c.Limits = Limits{Max: 2, CPUs: 6, MemoryMiB: 12288}
	return c
}
func TestCatalogDefaultAndBounds(t *testing.T) {
	c := catalog(t)
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	profiles := c.ProfileConfigs()
	if len(profiles) != 2 || profiles[0].ScaleSet != "chickadee" || profiles[0].CPUs != 4 || profiles[0].MemoryMiB != 8192 {
		t.Fatal("default did not resolve to medium")
	}
	for name, change := range map[string]func(*Config){
		"mixed":            func(c *Config) { c.CPUs = 2 },
		"unknown-image":    func(c *Config) { p := c.Profiles["chickadee"]; p.Image = "missing"; c.Profiles["chickadee"] = p },
		"unknown-resource": func(c *Config) { p := c.Profiles["chickadee"]; p.Resources = "missing"; c.Profiles["chickadee"] = p },
		"unfit":            func(c *Config) { c.Limits.MemoryMiB = 4096 },
		"warm-overcommit": func(c *Config) {
			p := c.Profiles["chickadee-small-ubuntu-2404"]
			p.Warm = 2
			c.Profiles["chickadee-small-ubuntu-2404"] = p
		},
		"unversioned": func(c *Config) { p := c.Images["ubuntu-2404"]; p.Version = "latest"; c.Images["ubuntu-2404"] = p },
		"typo-label":  func(c *Config) { c.Profiles["chickadee-small-ubtunu-2404"] = c.Profiles["chickadee-small-ubuntu-2404"] },
		"unsafe-unused-image": func(c *Config) {
			c.Images["unused"] = Image{Machine: "microvm", Path: "/var/lib/chickadee/images", DiskGiB: 16, OS: "rocky", Version: "10.2"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := catalog(t)
			change(&c)
			if c.Validate() == nil {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
}
