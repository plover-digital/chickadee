//go:build linux && amd64

package main

import (
	"strings"
	"testing"
)

func TestImageToolsEnvironmentDoesNotOverrideIdentityOrCredentials(t *testing.T) {
	for _, input := range []string{"HOME=/root", "GITHUB_TOKEN=secret", "USER=root", "LD_PRELOAD=/tmp/inject.so", strings.Repeat("x", 17000)} {
		if _, err := parseRunnerEnvironment(strings.NewReader(input)); err == nil {
			t.Fatal("unsafe image environment accepted")
		}
	}
	env, err := parseRunnerEnvironment(strings.NewReader("RUNNER_TOOL_CACHE=/opt/hostedtoolcache\nJAVA_HOME_17_X64=/usr/lib/jvm/java-17\n"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "HOME=/home/runner") || !strings.Contains(joined, "RUNNER_TOOL_CACHE=/opt/hostedtoolcache") {
		t.Fatal("identity/tool paths lost")
	}
}
