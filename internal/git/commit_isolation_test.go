package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCommit_RecordsOnlyTheGivenPaths pins that a release commit contains the
// release's own files and nothing else.
//
// A release commit is signed as the bot and lands on the branch the pipeline
// pushes. Committing the whole index makes that commit adopt whatever happened
// to be staged — another step's work, a hook's output, a developer's local
// staging — and publish it under the bot's name inside a `[skip ci]` commit
// nobody reviews. The chart's own files are the entire intended content.
func TestCommit_RecordsOnlyTheGivenPaths(t *testing.T) {
	// Given a release file to publish and unrelated work already staged.
	c, dir := initTestRepo(t)

	chartYAML := filepath.Join(dir, "charts", "app", "Chart.yaml")
	if err := os.MkdirAll(filepath.Dir(chartYAML), 0o750); err != nil {
		t.Fatalf("creating chart directory: %v", err)
	}
	if err := os.WriteFile(chartYAML, []byte("version: 0.2.0\n"), 0o600); err != nil {
		t.Fatalf("writing Chart.yaml: %v", err)
	}

	unrelated := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(unrelated, []byte("TOKEN=leaked\n"), 0o600); err != nil {
		t.Fatalf("writing unrelated file: %v", err)
	}
	wt, err := c.repo.Worktree()
	if err != nil {
		t.Fatalf("opening worktree: %v", err)
	}
	if _, err := wt.Add("secrets.env"); err != nil {
		t.Fatalf("staging unrelated file: %v", err)
	}

	// When the release commits its own path.
	if err := c.Commit("chore(app): release v0.2.0 [skip ci]", "bot", "bot@bot.com",
		"charts/app/Chart.yaml"); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	// Then the commit carries the release file and not the staged bystander.
	head, err := c.repo.Head()
	if err != nil {
		t.Fatalf("reading HEAD: %v", err)
	}
	commit, err := c.repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("reading commit: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("reading tree: %v", err)
	}

	if _, err := tree.File("charts/app/Chart.yaml"); err != nil {
		t.Errorf("release file missing from the release commit: %v", err)
	}
	if _, err := tree.File("secrets.env"); err == nil {
		t.Error("release commit absorbed an unrelated staged file")
	}
}

// TestCommit_PreservesUnrelatedStagingForItsOwner pins that the release leaves
// another actor's staged work staged, rather than consuming or discarding it.
func TestCommit_PreservesUnrelatedStagingForItsOwner(t *testing.T) {
	// Given unrelated work staged by someone else.
	c, dir := initTestRepo(t)

	chartYAML := filepath.Join(dir, "charts", "app", "Chart.yaml")
	if err := os.MkdirAll(filepath.Dir(chartYAML), 0o750); err != nil {
		t.Fatalf("creating chart directory: %v", err)
	}
	if err := os.WriteFile(chartYAML, []byte("version: 0.2.0\n"), 0o600); err != nil {
		t.Fatalf("writing Chart.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("wip\n"), 0o600); err != nil {
		t.Fatalf("writing unrelated file: %v", err)
	}
	wt, err := c.repo.Worktree()
	if err != nil {
		t.Fatalf("opening worktree: %v", err)
	}
	if _, err := wt.Add("notes.md"); err != nil {
		t.Fatalf("staging unrelated file: %v", err)
	}

	// When the release commits only its own path.
	if err := c.Commit("chore(app): release v0.2.0 [skip ci]", "bot", "bot@bot.com",
		"charts/app/Chart.yaml"); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	// Then the other actor's staging survives the release untouched.
	status, err := wt.Status()
	if err != nil {
		t.Fatalf("reading status: %v", err)
	}
	if got := status.File("notes.md").Staging; got != 'A' {
		t.Errorf("unrelated staging = %q, want %q: the release disturbed another actor's index",
			got, 'A')
	}
}
