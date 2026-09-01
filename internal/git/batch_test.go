package git

import (
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestCommitsSinceBatch_IncludesRootCommitAgainstEmptyTree(t *testing.T) {
	// Given
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}
	c := &Client{repo: repo}
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: introduce app")

	// When
	batch, err := c.CommitsSinceBatch([]ChartRange{{Chart: "app", Path: "charts/app"}})

	// Then
	if err != nil {
		t.Fatalf("CommitsSinceBatch() error = %v", err)
	}
	assertSubjects(t, batch["app"], "feat: introduce app")
}

func TestCommitsSinceBatch_SkipsMergePathsAndIncludesSideBranchCommit(t *testing.T) {
	// Given
	c, dir := initTestRepo(t)
	wt, err := c.repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	main, err := c.repo.Head()
	if err != nil {
		t.Fatalf("main head: %v", err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("side"),
		Create: true,
	}); err != nil {
		t.Fatalf("checkout side: %v", err)
	}
	addCommit(t, c, dir, "charts/app/values.yaml", "feat: side branch change")
	side, err := c.repo.Head()
	if err != nil {
		t.Fatalf("side head: %v", err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{Branch: main.Name()}); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	addCommit(t, c, dir, "README.md", "chore: main branch change")
	main, err = c.repo.Head()
	if err != nil {
		t.Fatalf("updated main head: %v", err)
	}
	mergeCommit(t, c, main, side, "merge: side branch")

	// When
	batch, err := c.CommitsSinceBatch([]ChartRange{{Chart: "app", Path: "charts/app"}})

	// Then
	if err != nil {
		t.Fatalf("CommitsSinceBatch() error = %v", err)
	}
	assertSubjects(t, batch["app"], "feat: side branch change")
}

func TestCommitsSinceBatch_LeavesDependentChartPresentAndEmpty_whenOnlyDependencyChanges(t *testing.T) {
	// Given
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: seed dependent chart")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("tag app: %v", err)
	}
	addCommit(t, c, dir, "charts/dependency/Chart.yaml", "feat: update dependency chart")

	// When
	batch, err := c.CommitsSinceBatch([]ChartRange{{
		Chart: "app",
		Tag:   "app-v0.1.0",
		Path:  "charts/app",
	}})

	// Then
	if err != nil {
		t.Fatalf("CommitsSinceBatch() error = %v", err)
	}
	if commits, present := batch["app"]; !present || len(commits) != 0 {
		t.Errorf("app = %v (present=%v), want a present empty result", Subjects(commits), present)
	}
}

func mergeCommit(t *testing.T, c *Client, main, side *plumbing.Reference, message string) {
	t.Helper()
	mainCommit, err := c.repo.CommitObject(main.Hash())
	if err != nil {
		t.Fatalf("read main commit: %v", err)
	}
	commit := &object.Commit{
		Author:       object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
		Committer:    object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
		Message:      message,
		ParentHashes: []plumbing.Hash{main.Hash(), side.Hash()},
		TreeHash:     mainCommit.TreeHash,
	}
	encoded := c.repo.Storer.NewEncodedObject()
	if err := commit.Encode(encoded); err != nil {
		t.Fatalf("encode merge commit: %v", err)
	}
	hash, err := c.repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatalf("store merge commit: %v", err)
	}
	if err := c.repo.Storer.SetReference(plumbing.NewHashReference(main.Name(), hash)); err != nil {
		t.Fatalf("set merge reference: %v", err)
	}
}

func assertSubjects(t *testing.T, commits []CommitInfo, want ...string) {
	t.Helper()
	got := Subjects(commits)
	if len(got) != len(want) {
		t.Fatalf("subjects = %v, want %v", got, want)
	}
	for index, subject := range want {
		if got[index] != subject {
			t.Errorf("subjects[%d] = %q, want %q", index, got[index], subject)
		}
	}
}

func TestCommitsSinceBatch_OneReadServesEveryChart(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/alpha/Chart.yaml", "feat: seed alpha")
	addCommit(t, c, dir, "charts/beta/Chart.yaml", "feat: seed beta")
	if err := c.Tag("alpha-v0.1.0"); err != nil {
		t.Fatalf("tag alpha: %v", err)
	}
	if err := c.Tag("beta-v0.1.0"); err != nil {
		t.Fatalf("tag beta: %v", err)
	}
	addCommit(t, c, dir, "charts/alpha/values.yaml", "feat: alpha gains tuning")
	addCommit(t, c, dir, "charts/beta/values.yaml", "fix: beta corrects a default")
	batch, err := c.CommitsSinceBatch([]ChartRange{
		{Chart: "alpha", Tag: "alpha-v0.1.0", Path: "charts/alpha"},
		{Chart: "beta", Tag: "beta-v0.1.0", Path: "charts/beta"},
	})
	if err != nil {
		t.Fatalf("CommitsSinceBatch() error = %v", err)
	}
	assertSubjects(t, batch["alpha"], "feat: alpha gains tuning")
	assertSubjects(t, batch["beta"], "fix: beta corrects a default")
}

func TestCommitsSinceBatch_MatchesPerChartResults(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/alpha/Chart.yaml", "feat: seed alpha")
	addCommit(t, c, dir, "charts/beta/Chart.yaml", "feat: seed beta")
	addCommit(t, c, dir, ".github/workflows/gates.yml", "fix(ci): adopt the action")
	if err := c.Tag("alpha-v0.1.0"); err != nil {
		t.Fatalf("tag alpha: %v", err)
	}
	if err := c.Tag("beta-v0.2.0"); err != nil {
		t.Fatalf("tag beta: %v", err)
	}
	addCommit(t, c, dir, "charts/alpha/values.yaml", "feat: alpha tuning")
	ranges := []ChartRange{{Chart: "alpha", Tag: "alpha-v0.1.0", Path: "charts/alpha"}, {Chart: "beta", Tag: "beta-v0.2.0", Path: "charts/beta"}}
	batch, err := c.CommitsSinceBatch(ranges)
	if err != nil {
		t.Fatalf("CommitsSinceBatch() error = %v", err)
	}
	for _, chartRange := range ranges {
		want, err := c.CommitsSince(chartRange.Tag, chartRange.Path)
		if err != nil {
			t.Fatalf("CommitsSince(%q) error = %v", chartRange.Chart, err)
		}
		assertSubjects(t, batch[chartRange.Chart], Subjects(want)...)
	}
}

func TestCommitsSinceBatch_ChartWithNoOwnCommitsIsEmpty(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/alpha/Chart.yaml", "feat: seed alpha")
	addCommit(t, c, dir, "charts/beta/Chart.yaml", "feat: seed beta")
	if err := c.Tag("alpha-v0.1.0"); err != nil {
		t.Fatalf("tag alpha: %v", err)
	}
	if err := c.Tag("beta-v0.1.0"); err != nil {
		t.Fatalf("tag beta: %v", err)
	}
	addCommit(t, c, dir, "charts/alpha/values.yaml", "feat: alpha tuning")
	batch, err := c.CommitsSinceBatch([]ChartRange{{Chart: "alpha", Tag: "alpha-v0.1.0", Path: "charts/alpha"}, {Chart: "beta", Tag: "beta-v0.1.0", Path: "charts/beta"}})
	if err != nil {
		t.Fatalf("CommitsSinceBatch() error = %v", err)
	}
	if commits, present := batch["beta"]; !present || len(commits) != 0 {
		t.Errorf("beta = %v (present=%v), want an empty entry", Subjects(commits), present)
	}
}

func TestCommitsSinceBatch_RespectsDirectoryBoundary(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/vault/Chart.yaml", "feat: seed vault")
	addCommit(t, c, dir, "charts/vault-extras/Chart.yaml", "feat: seed extras")
	if err := c.Tag("vault-v0.1.0"); err != nil {
		t.Fatalf("tag vault: %v", err)
	}
	addCommit(t, c, dir, "charts/vault-extras/values.yaml", "feat: extras only")
	batch, err := c.CommitsSinceBatch([]ChartRange{{Chart: "vault", Tag: "vault-v0.1.0", Path: "charts/vault"}})
	if err != nil {
		t.Fatalf("CommitsSinceBatch() error = %v", err)
	}
	assertSubjects(t, batch["vault"])
}
