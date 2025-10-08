package cherry

import (
	"context"
	"errors"
	"strings"
	testing "testing"
)

// ---- fake runner ----

type fakeRunner struct {
	clonedOwner string
	clonedRepo  string
	token       string

	cfgName  string
	cfgEmail string

	fetched []string

	coNew  string
	coFrom string

	pickedSHA      string
	pickedMainline int
	pushBranch     string

	errClone bool
	errCfg   bool
	errFetch bool
	errCO    bool
	errPick  error
	errPush  bool

	cleaned bool
}

func (f *fakeRunner) Clean() { f.cleaned = true }
func (f *fakeRunner) CloneWithToken(ctx context.Context, owner, repo, token string) error {
	f.clonedOwner, f.clonedRepo, f.token = owner, repo, token
	if f.errClone {
		return errors.New("clone failed")
	}
	return nil
}
func (f *fakeRunner) ConfigUser(ctx context.Context, name, email string) error {
	f.cfgName, f.cfgEmail = name, email
	if f.errCfg {
		return errors.New("config failed")
	}
	return nil
}
func (f *fakeRunner) Fetch(ctx context.Context, refs ...string) error {
	f.fetched = append(f.fetched, refs...)
	if f.errFetch {
		return errors.New("fetch failed")
	}
	return nil
}
func (f *fakeRunner) CheckoutBranchFrom(ctx context.Context, newBranch, fromRef string) error {
	f.coNew, f.coFrom = newBranch, fromRef
	if f.errCO {
		return errors.New("checkout failed")
	}
	return nil
}
func (f *fakeRunner) CherryPick(ctx context.Context, sha string) error {
	f.pickedSHA = sha
	return f.errPick
}
func (f *fakeRunner) CherryPickWithMainline(ctx context.Context, mainline int, sha string) error {
	f.pickedMainline, f.pickedSHA = mainline, sha
	return f.errPick
}
func (f *fakeRunner) Push(ctx context.Context, branch string) error {
	f.pushBranch = branch
	if f.errPush {
		return errors.New("push failed")
	}
	return nil
}

// helper to install fake newGitRunner and restore after
func withFakeRunner(t *testing.T, fr *fakeRunner) func() {
	t.Helper()
	orig := newGitRunner
	newGitRunner = func(cwd string, env ...string) (gitRunner, error) { return fr, nil }
	return func() { newGitRunner = orig }
}

// ---- tests ----

func TestDoCherryPick_Success(t *testing.T) {
	fr := &fakeRunner{}
	restore := withFakeRunner(t, fr)
	defer restore()

	actor := GitActor{Name: "bot", Email: "bot@noreply"}
	branch, err := DoCherryPick(context.Background(), "o", "r", "tok", "devops-release/0021", "abcdef123456", actor)
	if err != nil {
		t.Fatalf("DoCherryPick error: %v", err)
	}
	if fr.clonedOwner != "o" || fr.clonedRepo != "r" || fr.token != "tok" {
		t.Fatalf("clone args mismatch: %+v", fr)
	}
	if fr.cfgName != "bot" || fr.cfgEmail != "bot@noreply" {
		t.Fatalf("config user mismatch: %+v", fr)
	}
	// fetched sha + refs
	if len(fr.fetched) == 0 || !containsAll(fr.fetched,
		"master:refs/remotes/origin/master",
		"refs/heads/devops-release/0021:refs/remotes/origin/devops-release/0021",
		"abcdef123456",
	) {
		t.Fatalf("fetch refs mismatch: %#v", fr.fetched)
	}
	if fr.coFrom != "origin/devops-release/0021" {
		t.Fatalf("checkout from mismatch: %s", fr.coFrom)
	}
	if fr.pickedSHA != "abcdef123456" || fr.pickedMainline != 0 {
		t.Fatalf("pick mismatch: sha=%s mainline=%d", fr.pickedSHA, fr.pickedMainline)
	}
	if !strings.HasPrefix(fr.pushBranch, "autocherry/devops-release-0021/abcdef1") {
		t.Fatalf("push branch unexpected: %s", fr.pushBranch)
	}
	if branch != fr.pushBranch {
		t.Fatalf("returned branch mismatch: %s vs %s", branch, fr.pushBranch)
	}
	if !fr.cleaned {
		t.Fatalf("expected Clean() to be called via defer")
	}
}

func TestDoCherryPickWithMainline_Success(t *testing.T) {
	fr := &fakeRunner{}
	restore := withFakeRunner(t, fr)
	defer restore()

	actor := GitActor{Name: "bot", Email: "bot@noreply"}
	branch, err := DoCherryPickWithMainline(context.Background(), "o", "r", "tok", "devops-release/0021", "cafebabe1234567", 1, actor)
	if err != nil {
		t.Fatalf("DoCherryPickWithMainline error: %v", err)
	}
	if fr.pickedMainline != 1 || fr.pickedSHA != "cafebabe1234567" {
		t.Fatalf("expected mainline=1 pick; got mainline=%d sha=%s", fr.pickedMainline, fr.pickedSHA)
	}
	if !strings.HasPrefix(branch, "autocherry/devops-release-0021/cafebab") {
		t.Fatalf("unexpected work branch: %s", branch)
	}
}

func TestDoCherryPick_NoOpDetected(t *testing.T) {
	// Simulate git output for a no-op cherry-pick
	fr := &fakeRunner{errPick: errors.New("The previous cherry-pick is now empty, possibly due to conflict resolution.\nnothing to commit")}
	restore := withFakeRunner(t, fr)
	defer restore()

	actor := GitActor{Name: "bot", Email: "bot@noreply"}
	_, err := DoCherryPick(context.Background(), "o", "r", "tok", "devops-release/0021", "deadbeefc0ffee", actor)
	if !errors.Is(err, ErrNoopCherryPick) {
		t.Fatalf("want ErrNoopCherryPick, got %v", err)
	}
}

// small helper
func containsAll(slice []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, s := range slice {
			if s == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
