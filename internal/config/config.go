package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	GitHubURL      string `json:"github_url"`
	ClientID       string `json:"app_client_id"`
	InstallationID int64  `json:"app_installation_id"`
	KeyFile        string `json:"app_key_file"`
	ScaleSet       string `json:"scale_set"`
	RunnerGroupID  int    `json:"runner_group_id"`
	Warm           int    `json:"warm_pool"`
	Max            int    `json:"max_vms"`
	CPUs           int    `json:"cpus"`
	MemoryMiB      int    `json:"memory_mib"`
	DiskGiB        int    `json:"disk_gib"`
	BootSeconds    int    `json:"boot_timeout_seconds"`
	JobSeconds     int    `json:"job_timeout_seconds"`
	StateDir       string `json:"state_dir"`
	ImageDir       string `json:"image_dir"`
}

var Name = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

func Load(path string) (Config, error) {
	var c Config
	f, e := os.Open(path)
	if e != nil {
		return c, e
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	if d.Decode(new(any)) != io.EOF {
		return c, fmt.Errorf("trailing config data")
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	u, e := url.Parse(c.GitHubURL)
	if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return fmt.Errorf("use a public GitHub https org/repo URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" || parts[0] == "enterprises" {
		return fmt.Errorf("use repository or organization scope")
	}
	for _, p := range parts {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(p) {
			return fmt.Errorf("invalid GitHub scope")
		}
	}

	if !Name.MatchString(c.ScaleSet) || c.Warm < 0 || c.Warm > c.Max || c.Max < 1 || c.Max > 32 || c.CPUs < 1 || c.CPUs > 16 || c.MemoryMiB < 512 || c.MemoryMiB > 32768 || c.DiskGiB < 4 || c.DiskGiB > 128 || c.BootSeconds < 10 || c.BootSeconds > 600 || c.JobSeconds < 60 || c.JobSeconds > 86400 {
		return fmt.Errorf("invalid name, capacity, resources, or timeout")
	}
	if c.ImageDir == c.StateDir || strings.HasPrefix(c.ImageDir, c.StateDir+"/") || strings.HasPrefix(c.StateDir, c.ImageDir+"/") {
		return fmt.Errorf("image and state directories must be separate and nonoverlapping")
	}
	if strings.ContainsAny(c.StateDir+c.ImageDir, ",\n\r") {
		return fmt.Errorf("image and state paths cannot contain QEMU delimiters")
	}
	if filepath.Clean(c.StateDir) != c.StateDir || filepath.Clean(c.ImageDir) != c.ImageDir || c.StateDir == "/" || c.ImageDir == "/" {
		return fmt.Errorf("use clean, dedicated image and state directories")
	}
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.ImageDir) || !filepath.IsAbs(c.KeyFile) || len(c.StateDir) > 50 {
		return fmt.Errorf("use absolute paths; state_dir must be at most 50 bytes for Unix sockets")
	}
	if c.ClientID == "" || c.InstallationID <= 0 || c.RunnerGroupID < 1 {
		return fmt.Errorf("GitHub App and runner group required")
	}
	return nil
}
