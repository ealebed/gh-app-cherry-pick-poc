package cherry

import (
	"regexp"
	"strings"

	github "github.com/google/go-github/v75/github"
)

// Matches variations like:
//
//	"cherry-pick to devops-release/0021"
//	"cherrypick to refs/heads/devops-release/0021"
//	"cherry pick to devops-release/0021, devops-release/0022"
var reCherryTo = regexp.MustCompile(`(?i)^\s*cherry[\s-]?pick\s+to\s+(.+?)\s*$`)

// ParseTargetBranches extracts target branch names from PR labels.
// It returns cleaned branch names (without "refs/heads/"), de-duplicated.
func ParseTargetBranches(labels []*github.Label) []string {
	var out []string
	seen := make(map[string]struct{})

	for _, l := range labels {
		if l == nil || l.Name == nil {
			continue
		}
		name := strings.TrimSpace(l.GetName())
		m := reCherryTo.FindStringSubmatch(name)
		if len(m) != 2 {
			continue
		}

		// Support multiple branches separated by commas or whitespace.
		candidates := splitBranches(m[1])
		for _, br := range candidates {
			br = strings.TrimSpace(br)
			br = strings.TrimPrefix(br, "refs/heads/")
			if br == "" {
				continue
			}
			if _, ok := seen[br]; ok {
				continue
			}
			seen[br] = struct{}{}
			out = append(out, br)
		}
	}
	return out
}

func splitBranches(s string) []string {
	// First split by comma, then split any remaining by whitespace.
	var res []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Allow "devops-release/0021 devops-release/0022" (space separated)
		for _, sub := range strings.Fields(part) {
			if sub != "" {
				res = append(res, sub)
			}
		}
	}
	return res
}
