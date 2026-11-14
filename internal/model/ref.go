package model

import "strings"

// ValidBranchRef validates a fully qualified branch ref without invoking Git
// while reading durable state. Git remains the authority when observing remotes.
func ValidBranchRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/heads/") || strings.HasSuffix(ref, ".") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.ContainsAny(ref, "~^:?*[\\") {
		return false
	}
	for i := 0; i < len(ref); i++ {
		if ref[i] <= 32 || ref[i] == 127 {
			return false
		}
	}
	for _, component := range strings.Split(ref, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}
