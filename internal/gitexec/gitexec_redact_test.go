package gitexec

import "testing"

func TestTokenRedaction(t *testing.T) {
	in := "https://x-access-token:ghs_ABCDEF123@github.com/ealebed/observer.git"
	out := reToken.ReplaceAllString(in, "x-access-token:***@")
	want := "https://x-access-token:***@github.com/ealebed/observer.git"
	if out != want {
		t.Fatalf("redaction failed: got %q want %q", out, want)
	}
}
