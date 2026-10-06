//go:build linux && amd64

package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

// Image-owned environment supplies tools, never inherited host/bootstrap state.
func parseRunnerEnvironment(reader io.Reader) ([]string, error) {
	values := map[string]string{"HOME": "/home/runner", "USER": "runner", "PATH": "/usr/local/bin:/usr/bin:/bin", "LANG": "C.UTF-8"}
	allowed := map[string]bool{"PATH": true, "RUNNER_TOOL_CACHE": true, "AGENT_TOOLSDIRECTORY": true, "JAVA_HOME": true, "ANDROID_HOME": true, "ANDROID_SDK_ROOT": true, "ANDROID_NDK_HOME": true, "CONDA": true, "VCPKG_INSTALLATION_ROOT": true, "CHROMEWEBDRIVER": true, "GECKOWEBDRIVER": true, "SELENIUM_JAR_PATH": true, "DOTNET_ROOT": true, "DOTNET_MULTILEVEL_LOOKUP": true, "DOTNET_NOLOGO": true, "DOTNET_CLI_TELEMETRY_OPTOUT": true, "CARGO_HOME": true, "RUSTUP_HOME": true, "ImageOS": true, "ImageVersion": true}
	scanner := bufio.NewScanner(io.LimitReader(reader, 16385))
	scanner.Buffer(make([]byte, 1024), 4096)
	total, lines := 0, 0
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		lines++
		if total > 16384 || lines > 64 {
			return nil, errors.New("image environment exceeds limit")
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.ContainsRune(value, '\x00') {
			return nil, errors.New("invalid image environment")
		}
		if !allowed[key] {
			if !strings.HasPrefix(key, "JAVA_HOME_") || !strings.HasSuffix(key, "_X64") {
				return nil, errors.New("image environment key not allowed")
			}
		}
		values[key] = value
	}
	if scanner.Err() != nil {
		return nil, errors.New("invalid image environment")
	}
	result := make([]string, 0, len(values))
	for k, v := range values {
		result = append(result, k+"="+v)
	}
	return result, nil
}
func runnerEnvironment() ([]string, error) {
	path := "/etc/chickadee/runner.env"
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return parseRunnerEnvironment(strings.NewReader(""))
	}
	if err != nil {
		return nil, errors.New("image environment unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 16384 {
		return nil, errors.New("image environment must be regular, root owned and not writable by runner")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("image environment unavailable")
	}
	defer file.Close()
	return parseRunnerEnvironment(file)
}
func runnerCredential() (*syscall.Credential, error) {
	runner, err := user.Lookup("runner")
	if err != nil || runner.Uid != "1000" || runner.Gid != "1000" {
		return nil, errors.New("image runner identity must be UID/GID 1000")
	}
	ids, err := runner.GroupIds()
	if err != nil || len(ids) > 32 {
		return nil, errors.New("runner group lookup failed")
	}
	groups := []uint32{}
	for _, id := range ids {
		gid, err := strconv.ParseUint(id, 10, 32)
		if err != nil || gid == 0 {
			return nil, errors.New("invalid runner group")
		}
		groups = append(groups, uint32(gid))
	}
	return &syscall.Credential{Uid: 1000, Gid: 1000, Groups: groups}, nil
}
