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

func TestImageTrustEnvironmentUsesOnlyCompleteVendorStorePolicy(t *testing.T) {
	for _, pair := range [][2]string{{"/etc/ssl/certs/ca-certificates.crt", "/etc/ssl/certs"}, {"/etc/pki/tls/certs/ca-bundle.crt", "/etc/pki/tls/certs"}} {
		input := "NODE_OPTIONS=--use-openssl-ca\nSSL_CERT_FILE=" + pair[0] + "\nSSL_CERT_DIR=" + pair[1] + "\n"
		env, err := parseRunnerEnvironment(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(input), "\n") {
			if !strings.Contains(strings.Join(env, "\n"), line) {
				t.Fatal("trust control lost", line)
			}
		}
	}
	for _, input := range []string{
		"NODE_OPTIONS=--require=/tmp/code.js",
		"NODE_OPTIONS=--use-openssl-ca",
		"SSL_CERT_FILE=/tmp/roots.pem",
		"NODE_EXTRA_CA_CERTS=/tmp/roots.pem",
		"NODE_OPTIONS=--use-openssl-ca\nSSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt\nSSL_CERT_DIR=/tmp/roots",
	} {
		if _, err := parseRunnerEnvironment(strings.NewReader(input)); err == nil {
			t.Fatal("unsafe/incomplete trust control accepted", input)
		}
	}
}
