package preflight

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestReportWithholdsUntrustedErrorText(t *testing.T) {
	secret := "private/path/token=secret\nFORGED remote diagnostic"
	for _, err := range []error{errors.New(secret), fmt.Errorf("%s: %w", secret, New("workspace-permissions"))} {
		code, remedy := Report(err)
		if strings.Contains(code+remedy, "secret") || strings.Contains(code+remedy, "FORGED") || strings.Contains(code+remedy, "private/path") {
			t.Fatal("untrusted detail leaked")
		}
	}
	code, _ := Report(fmt.Errorf("path: %w", New("workspace-permissions")))
	if code != "workspace-permissions" {
		t.Fatal(code)
	}
	code, _ = Report(New(secret))
	if code != "config-or-preflight-invalid" {
		t.Fatal(code)
	}
}
