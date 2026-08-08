package git

import (
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// TestLatestTag_IgnoresTagUnreachableFromHead pins that the baseline is the
// highest release REACHABLE from HEAD, not the highest tag in the repository.
//
// A tag on a branch that HEAD never integrated describes a release line HEAD is
// not on. Choosing it as the baseline compares this chart against work that is
// absent from HEAD, so the resulting range is not `<tag>..HEAD` at all.
func TestLatestTag_IgnoresTagUnreachableFromHead(t *testing.T) {
	// Given a released chart on the primary line.
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: seed app")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("tag app-v0.1.0: %v", err)
	}
	main, err := c.repo.Head()
	if err != nil {
		t.Fatalf("main head: %v", err)
	}

	// And a higher tag cut on a branch HEAD never integrated.
	wt, err := c.repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("side"),
		Create: true,
	}); err != nil {
		t.Fatalf("checkout side: %v", err)
	}
	addCommit(t, c, dir, "charts/app/values.yaml", "feat: side line work")
	if err := c.Tag("app-v0.9.0"); err != nil {
		t.Fatalf("tag app-v0.9.0: %v", err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{Branch: main.Name()}); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	// When the baseline is resolved for the chart.
	tag, err := c.LatestTag("app")
	if err != nil {
		t.Fatalf("LatestTag() error = %v", err)
	}

	// Then the unreachable higher tag is not the baseline.
	if tag != "app-v0.1.0" {
		t.Errorf("LatestTag() = %q, want %q: app-v0.9.0 is not reachable from HEAD",
			tag, "app-v0.1.0")
	}
}

// TestCommitsSinceBatch_ExcludesOnlyReachableBaselineHistory pins that batch
// attribution keeps `<tag>..HEAD` semantics when a chart's baseline lives on a
// line HEAD never integrated: work that IS on HEAD must still be attributed.
func TestCommitsSinceBatch_ExcludesOnlyReachableBaselineHistory(t *testing.T) {
	// Given a chart released on the primary line.
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: seed app")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("tag app-v0.1.0: %v", err)
	}
	main, err := c.repo.Head()
	if err != nil {
		t.Fatalf("main head: %v", err)
	}

	// And an abandoned branch carrying its own chart work and tag.
	wt, err := c.repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("side"),
		Create: true,
	}); err != nil {
		t.Fatalf("checkout side: %v", err)
	}
	addCommit(t, c, dir, "charts/app/values.yaml", "feat: abandoned line work")
	if err := c.Tag("app-v0.9.0"); err != nil {
		t.Fatalf("tag app-v0.9.0: %v", err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{Branch: main.Name()}); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	// And genuinely new work on the primary line after the baseline.
	addCommit(t, c, dir, "charts/app/limits.yaml", "feat: primary line work")

	// When the range is read for the reachable baseline.
	batch, err := c.CommitsSinceBatch([]ChartRange{
		{Chart: "app", Tag: "app-v0.1.0", Path: "charts/app"},
	})
	if err != nil {
		t.Fatalf("CommitsSinceBatch() error = %v", err)
	}

	// Then only the work present on HEAD is attributed.
	assertSubjects(t, batch["app"], "feat: primary line work")
}
