package site

import "regexp"

var workflowPathPattern = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9_-][A-Za-z0-9_.-]{0,99}\.ya?ml$`)

func validWorkflowPath(path string) bool { return workflowPathPattern.MatchString(path) }
func (e Enrollment) RequestedWorkflowRef() string {
	if e.Account.Type != "Organization" || !validWorkflowPath(e.WorkflowPath) {
		return ""
	}
	return e.Repository.Name + "/" + e.WorkflowPath + "@refs/heads/main"
}
func (e Enrollment) WorkflowChangesPending() bool {
	return e.Account.Type == "Organization" && e.EnabledWorkflowAccess != "repository" && e.WorkflowPath != "" && e.WorkflowPath != e.EnabledWorkflowPath
}
func (e Enrollment) WorkflowFile() string {
	if validWorkflowPath(e.EnabledWorkflowPath) {
		return e.EnabledWorkflowPath
	}
	return ".github/workflows/chickadee.yml"
}

func validWorkflowAccess(access string) bool {
	return access == "" || access == "workflow" || access == "repository"
}
