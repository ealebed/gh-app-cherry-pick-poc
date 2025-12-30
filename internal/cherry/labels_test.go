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

func Test_splitBranches(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "comma separated",
			input: "branch1, branch2, branch3",
			want:  []string{"branch1", "branch2", "branch3"},
		},
		{
			name:  "space separated",
			input: "branch1 branch2 branch3",
			want:  []string{"branch1", "branch2", "branch3"},
		},
		{
			name:  "mixed comma and space",
			input: "branch1, branch2 branch3",
			want:  []string{"branch1", "branch2", "branch3"},
		},
		{
			name:  "single branch",
			input: "branch1",
			want:  []string{"branch1"},
		},
		{
			name:  "empty string",
			input: "",
			want:  []string{},
		},
		{
			name:  "whitespace only",
			input: "   ",
			want:  []string{},
		},
		{
			name:  "with extra spaces",
			input: "  branch1  ,  branch2  ",
			want:  []string{"branch1", "branch2"},
		},
		{
			name:  "multiple spaces between branches",
			input: "branch1    branch2",
			want:  []string{"branch1", "branch2"},
		},
		{
			name:  "empty segments",
			input: "branch1,,branch2",
			want:  []string{"branch1", "branch2"},
		},
		{
			name:  "trailing comma",
			input: "branch1, branch2,",
			want:  []string{"branch1", "branch2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitBranches(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("len mismatch: got=%v want=%v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("idx %d: got=%q want=%q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
