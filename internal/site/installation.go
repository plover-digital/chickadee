package site

import (
	"fmt"
	"net/url"
)

// ManagementURL uses only the installation identity verified through GitHub's
// authenticated API, never a browser callback installation_id query.
func (i Installation) ManagementURL() string {
	if i.ID <= 0 {
		return ""
	}
	switch i.Account.Type {
	case "Organization":
		if i.Account.Login == "" {
			return ""
		}
		return fmt.Sprintf("https://github.com/organizations/%s/settings/installations/%d", url.PathEscape(i.Account.Login), i.ID)
	case "User":
		return fmt.Sprintf("https://github.com/settings/installations/%d", i.ID)
	default:
		return ""
	}
}
