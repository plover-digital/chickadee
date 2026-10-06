package site

import (
	"testing"
)

func TestVerifiedInstallationManagementURL(t *testing.T) {
	for _, tc := range []struct {
		i    Installation
		want string
	}{
		{Installation{ID: 123, Account: Account{Login: "example-org", Type: "Organization"}}, "https://github.com/organizations/example-org/settings/installations/123"},
		{Installation{ID: 456, Account: Account{Login: "tester", Type: "User"}}, "https://github.com/settings/installations/456"},
		{Installation{ID: 123, Account: Account{Login: "bad/path", Type: "Organization"}}, "https://github.com/organizations/bad%2Fpath/settings/installations/123"},
		{Installation{ID: 0, Account: Account{Type: "User"}}, ""},
		{Installation{ID: 123, Account: Account{Type: "Organization"}}, ""},
	} {
		if got := tc.i.ManagementURL(); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
}
