package site

import "fmt"

// ServiceView groups organization repositories sharing one runner scope. Its
// representative enrollment retains the existing operator handoff identity.
type ServiceView struct {
	Enrollment
	Organization bool
	Repositories []Repository
}

func (s ServiceView) DisplayName() string {
	if s.Organization {
		return s.Account.Login
	}
	return s.Repository.Name
}
func (p page) Services() []ServiceView {
	var out []ServiceView
	groups := map[string]int{}
	for _, entry := range p.Enrollments {
		organization := entry.Account.Type == "Organization"
		authorized := false
		for _, choice := range p.Choices {
			if choice.Installation.ID == entry.InstallationID && choice.Repository.ID == entry.Repository.ID {
				authorized = true
				break
			}
		}
		key := entry.ID
		if organization {
			key = fmt.Sprintf("org:%d:%d", entry.InstallationID, entry.Account.ID)
		}
		index, exists := groups[key]
		if !exists {
			index = len(out)
			groups[key] = index
			out = append(out, ServiceView{Enrollment: entry, Organization: organization})
		}
		service := &out[index]
		if authorized {
			found := false
			for _, repo := range service.Repositories {
				if repo.ID == entry.Repository.ID {
					found = true
					break
				}
			}
			if !found {
				service.Repositories = append(service.Repositories, entry.Repository)
			}
			// Scope usage is repeated per enrollment by the relay; take one latest
			// snapshot rather than summing it and double-counting VM minutes.
			if service.Status == "permission-required" || entry.Updated.After(service.Updated) {
				service.Enrollment = entry
			}
		}
	}
	return out
}

// SetupChoices presents one queue editor per organization installation while
// personal installations retain their independent repository queues.
func (p page) SetupChoices() []Choice {
	var out []Choice
	groups := map[int64]int{}
	for _, choice := range p.Choices {
		if choice.Installation.Account.Type != "Organization" {
			out = append(out, choice)
			continue
		}
		if index, exists := groups[choice.Installation.ID]; exists {
			if len(out[index].SelectedQueues) == 0 && len(choice.SelectedQueues) > 0 {
				out[index] = choice
			}
			continue
		}
		groups[choice.Installation.ID] = len(out)
		out = append(out, choice)
	}
	return out
}
