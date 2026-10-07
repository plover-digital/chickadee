package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Image struct {
	Machine string `json:"machine"`
	Path    string `json:"path"`
	DiskGiB int    `json:"disk_gib"`
	OS      string `json:"os"`
	Version string `json:"version"`
}
type Resources struct {
	CPUs      int `json:"cpus"`
	MemoryMiB int `json:"memory_mib"`
}
type Profile struct {
	Image     string `json:"image"`
	Resources string `json:"resources"`
	Warm      int    `json:"warm_pool"`
	Max       int    `json:"max_vms"`
}
type Limits struct {
	Max       int `json:"max_vms"`
	CPUs      int `json:"max_vcpus"`
	MemoryMiB int `json:"max_memory_mib"`
}
type Scope struct {
	Disabled       bool               `json:"disabled,omitempty"`
	Max            int                `json:"max_vms,omitempty"`
	GitHubURL      string             `json:"github_url"`
	InstallationID int64              `json:"app_installation_id"`
	RunnerGroupID  int                `json:"runner_group_id"`
	Profiles       map[string]Profile `json:"profiles"`
}
type Config struct {
	// AssignedCPUs is local execution metadata, never accepted from JSON.
	AssignedCPUs []int            `json:"-"`
	Scopes       map[string]Scope `json:"scopes,omitempty"`
	scopePool    bool
	scopeMax     int

	Machine         string               `json:"machine,omitempty"`
	Images          map[string]Image     `json:"images,omitempty"`
	ResourceClasses map[string]Resources `json:"resource_classes,omitempty"`
	Profiles        map[string]Profile   `json:"profiles,omitempty"`
	Limits          Limits               `json:"limits,omitempty"`
	GitHubURL       string               `json:"github_url"`
	ClientID        string               `json:"app_client_id"`
	InstallationID  int64                `json:"app_installation_id"`
	KeyFile         string               `json:"app_key_file"`
	ScaleSet        string               `json:"scale_set"`
	RunnerGroupID   int                  `json:"runner_group_id"`
	Warm            int                  `json:"warm_pool"`
	Max             int                  `json:"max_vms"`
	CPUs            int                  `json:"cpus"`
	MemoryMiB       int                  `json:"memory_mib"`
	DiskGiB         int                  `json:"disk_gib"`
	BootSeconds     int                  `json:"boot_timeout_seconds"`
	JobSeconds      int                  `json:"job_timeout_seconds"`
	StateDir        string               `json:"state_dir"`
	ImageDir        string               `json:"image_dir"`
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

// ProfileConfigs resolves the catalog once; workers receive immutable flat configs.
// Key identifies a queue inside its GitHub scope. Labels can repeat across scopes.
func (c Config) Key() string {
	if c.scopePool {
		return ScopeKey(c.GitHubURL, c.ScaleSet)
	}
	return c.ScaleSet
}

// ScopeLimit bounds combined VM allocations across a scope's queues.
func (c Config) ScopeLimit() int {
	if c.scopeMax > 0 {
		return c.scopeMax
	}
	return 32
}
func ScopeKey(scope, label string) string {
	return strings.ToLower(strings.TrimRight(scope, "/")) + "|" + label
}
func (c Config) ProfileConfigs() []Config {
	if len(c.Scopes) > 0 {
		names := make([]string, 0, len(c.Scopes))
		for name := range c.Scopes {
			names = append(names, name)
		}
		sort.Strings(names)
		var out []Config
		for _, name := range names {
			scope := c.Scopes[name]
			if scope.Disabled {
				continue
			}
			bound := c
			bound.Scopes = nil
			bound.GitHubURL = scope.GitHubURL
			bound.InstallationID = scope.InstallationID
			bound.RunnerGroupID = scope.RunnerGroupID
			bound.Profiles = scope.Profiles
			bound.scopePool = true
			bound.scopeMax = scope.Max
			out = append(out, bound.ProfileConfigs()...)
		}
		return out
	}
	if len(c.Profiles) == 0 {
		return []Config{c}
	}
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Config, 0, len(names))
	for _, name := range names {
		p := c.Profiles[name]
		image := c.Images[p.Image]
		r := c.ResourceClasses[p.Resources]
		v := c
		v.Images = nil
		v.ResourceClasses = nil
		v.Profiles = nil
		v.Limits = Limits{}
		v.ScaleSet = name
		v.ImageDir = image.Path
		v.Machine = image.Machine
		v.DiskGiB = image.DiskGiB
		v.CPUs = r.CPUs
		v.MemoryMiB = r.MemoryMiB
		v.Warm = p.Warm
		v.Max = p.Max
		out = append(out, v)
	}
	return out
}
func (c Config) HostLimits() Limits {
	if len(c.Profiles) > 0 || len(c.Scopes) > 0 {
		return c.Limits
	}
	return Limits{Max: c.Max, CPUs: c.Max * c.CPUs, MemoryMiB: c.Max * c.MemoryMiB}
}
func (c Config) Validate() error {
	if len(c.Scopes) > 0 {
		return c.validateScopes()
	}
	if len(c.Profiles) > 0 {
		return c.validateCatalog()
	}
	if len(c.Images) > 0 || len(c.ResourceClasses) > 0 || c.Limits != (Limits{}) {
		return fmt.Errorf("catalog requires profiles")
	}

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

	if c.Machine != "" && c.Machine != "microvm" && c.Machine != "q35" {
		return fmt.Errorf("machine must be microvm or q35")
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

func (c Config) validateCatalog() error {
	if len(c.Profiles) > 16 || len(c.Images) == 0 || len(c.Images) > 16 || len(c.ResourceClasses) == 0 || len(c.ResourceClasses) > 16 {
		return fmt.Errorf("catalog needs 1..16 profiles, images and resource classes")
	}
	if c.Machine != "" || c.ScaleSet != "" || c.ImageDir != "" || c.Max != 0 || c.Warm != 0 || c.CPUs != 0 || c.MemoryMiB != 0 || c.DiskGiB != 0 {
		return fmt.Errorf("do not mix flat resources with a profile catalog")
	}
	l := c.Limits
	if l.Max < 1 || l.Max > 32 || l.CPUs < 1 || l.CPUs > 512 || l.MemoryMiB < 512 || l.MemoryMiB > 1048576 {
		return fmt.Errorf("invalid aggregate host limits")
	}
	for name, image := range c.Images {
		if !Name.MatchString(name) || !Name.MatchString(image.OS) || !regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,2}$`).MatchString(image.Version) {
			return fmt.Errorf("image needs valid name, OS and explicit dotted version")
		}
		if image.Machine != "microvm" && image.Machine != "q35" {
			return fmt.Errorf("catalog image must specify microvm or q35")
		}
		if image.DiskGiB < 4 || image.DiskGiB > 128 {
			return fmt.Errorf("invalid image disk size")
		}
		// Validate even unused catalog entries rather than allow dormant unsafe paths.
		v := c
		v.Images = nil
		v.ResourceClasses = nil
		v.Profiles = nil
		v.Limits = Limits{}
		v.ScaleSet = "image-check"
		v.ImageDir = image.Path
		v.Machine = image.Machine
		v.DiskGiB = image.DiskGiB
		v.Max = 1
		v.CPUs = 1
		v.MemoryMiB = 512
		if e := v.Validate(); e != nil {
			return fmt.Errorf("image %s: %w", name, e)
		}
	}
	for name, r := range c.ResourceClasses {
		if !Name.MatchString(name) || r.CPUs < 1 || r.CPUs > 16 || r.MemoryMiB < 512 || r.MemoryMiB > 32768 {
			return fmt.Errorf("invalid resource class")
		}
	}
	warmCount, warmCPU, warmRAM := 0, 0, 0
	for name, p := range c.Profiles {
		if !Name.MatchString(name) {
			return fmt.Errorf("invalid profile name")
		}
		image, ok := c.Images[p.Image]
		if !ok {
			return fmt.Errorf("profile references unknown image")
		}
		r, ok := c.ResourceClasses[p.Resources]
		if !ok {
			return fmt.Errorf("profile references unknown resources")
		}
		// chickadee is a first-class default, not a label parser or hidden fallback.
		expected := "chickadee-" + p.Resources + "-" + image.OS + "-" + strings.ReplaceAll(image.Version, ".", "")
		if name != "chickadee" && name != expected {
			return fmt.Errorf("profile name must be chickadee or %s", expected)
		}
		if p.Max < 1 || p.Max > l.Max || p.Warm < 0 || p.Warm > p.Max || r.CPUs > l.CPUs || r.MemoryMiB > l.MemoryMiB {
			return fmt.Errorf("profile cannot fit host limits")
		}
		warmCount += p.Warm
		warmCPU += p.Warm * r.CPUs
		warmRAM += p.Warm * r.MemoryMiB
	}
	if warmCount > l.Max || warmCPU > l.CPUs || warmRAM > l.MemoryMiB {
		return fmt.Errorf("combined warm targets exceed host limits")
	}
	for _, v := range c.ProfileConfigs() {
		if e := v.Validate(); e != nil {
			return fmt.Errorf("profile %s: %w", v.ScaleSet, e)
		}
	}
	return nil
}

func (c Config) validateScopes() error {
	if len(c.Scopes) > 16 || len(c.Profiles) > 0 || c.GitHubURL != "" || c.InstallationID != 0 || c.RunnerGroupID != 0 {
		return fmt.Errorf("scopes replace the top-level GitHub scope and profiles")
	}
	seen := map[string]bool{}
	count, cpu, ram := 0, 0, 0
	queues := 0
	for name, scope := range c.Scopes {
		if scope.Max < 0 || scope.Max > c.Limits.Max {
			return fmt.Errorf("scope concurrency exceeds host limit")
		}
		if !Name.MatchString(name) || len(scope.Profiles) == 0 {
			return fmt.Errorf("scope needs a valid name and profiles")
		}
		canonical := strings.ToLower(strings.TrimRight(scope.GitHubURL, "/"))
		if seen[canonical] {
			return fmt.Errorf("duplicate GitHub scope")
		}
		seen[canonical] = true
		bound := c
		bound.Scopes = nil
		bound.GitHubURL = scope.GitHubURL
		bound.InstallationID = scope.InstallationID
		bound.RunnerGroupID = scope.RunnerGroupID
		bound.Profiles = scope.Profiles
		if e := bound.Validate(); e != nil {
			return fmt.Errorf("scope %s: %w", name, e)
		}
		scopeWarm := 0
		for _, p := range bound.ProfileConfigs() {
			scopeWarm += p.Warm
			if !scope.Disabled {
				count += p.Warm
				cpu += p.Warm * p.CPUs
				ram += p.Warm * p.MemoryMiB
			}
			queues++
		}
		if scope.Max > 0 && scopeWarm > scope.Max {
			return fmt.Errorf("scope warm pool exceeds its concurrency limit")
		}
	}
	if len(c.ProfileConfigs()) == 0 {
		return fmt.Errorf("at least one operator scope must remain enabled")
	}
	if queues > 64 || count > c.Limits.Max || cpu > c.Limits.CPUs || ram > c.Limits.MemoryMiB {
		return fmt.Errorf("combined scope queue/warm budgets exceeded")
	}
	return nil
}
