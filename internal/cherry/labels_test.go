package cherry

import (
	"testing"

	github "github.com/google/go-github/v75/github"
)

func L(name string) *github.Label { return &github.Label{Name: github.Ptr(name)} }

func TestParseTargetBranches(t *testing.T) {
	cases := []struct {
		name   string
		labels []*github.Label
		want   []string
	}{
		{
			name:   "single",
			labels: []*github.Label{L("cherry-pick to devops-release/0021")},
			want:   []string{"devops-release/0021"},
		},
		{
			name:   "case-insensitive",
			labels: []*github.Label{L("ChErRy-PiCk To devops-release/0022")},
			want:   []string{"devops-release/0022"},
		},
		{
			name:   "refs/heads",
			labels: []*github.Label{L("cherry pick to refs/heads/devops-release/0030")},
			want:   []string{"devops-release/0030"},
		},
		{
			name:   "comma separated",
			labels: []*github.Label{L("cherry-pick to devops-release/0021, devops-release/0022")},
			want:   []string{"devops-release/0021", "devops-release/0022"},
		},
		{
			name:   "space separated",
			labels: []*github.Label{L("cherry-pick to devops-release/0021 devops-release/0022")},
			want:   []string{"devops-release/0021", "devops-release/0022"},
		},
		{
			name:   "dedupe",
			labels: []*github.Label{L("cherry-pick to devops-release/0021"), L("cherry-pick to devops-release/0021")},
			want:   []string{"devops-release/0021"},
		},
		{
			name:   "ignore others",
			labels: []*github.Label{L("bug"), L("enhancement"), L("cherrypick to devops-release/0042")},
			want:   []string{"devops-release/0042"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTargetBranches(tc.labels)
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got=%v want=%v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("idx %d: got=%q want=%q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
