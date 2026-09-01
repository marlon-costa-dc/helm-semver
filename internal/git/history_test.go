package git

import "testing"

func TestCommitsSince_ExcludesTagAncestryWhenTagCommitOutsidePathFilter(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: add app chart")
	addCommit(t, c, dir, ".github/workflows/gates.yml", "fix(ci): use action")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("tag app: %v", err)
	}
	addCommit(t, c, dir, "charts/app/values.yaml", "chore: retune limits")

	commits, err := c.CommitsSince("app-v0.1.0", "charts/app")
	if err != nil {
		t.Fatalf("CommitsSince() error = %v", err)
	}
	assertSubjects(t, commits, "chore: retune limits")
}

func TestCommitsSince_RespectsDirectoryBoundary(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/vault/Chart.yaml", "feat: vault only")
	addCommit(t, c, dir, "charts/vault-extras/Chart.yaml", "feat: extras only")

	commits, err := c.CommitsSince("", "charts/vault")
	if err != nil {
		t.Fatalf("CommitsSince() error = %v", err)
	}
	assertSubjects(t, commits, "feat: vault only")
}

func TestLatestTag_OrdersSemver(t *testing.T) {
	c, dir := initTestRepo(t)
	for _, tag := range []string{"myapp-v0.4.9", "myapp-v0.4.99", "myapp-v0.4.120"} {
		addCommit(t, c, dir, "charts/myapp/Chart.yaml", "chore: release "+tag)
		if err := c.Tag(tag); err != nil {
			t.Fatalf("tag %q: %v", tag, err)
		}
	}

	tag, err := c.LatestTag("myapp")
	if err != nil {
		t.Fatalf("LatestTag() error = %v", err)
	}
	if tag != "myapp-v0.4.120" {
		t.Errorf("LatestTag() = %q, want %q", tag, "myapp-v0.4.120")
	}
}

func TestLatestTag_IgnoresUnparseableTags(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/myapp/Chart.yaml", "feat: initial")
	if err := c.Tag("myapp-v0.1.0"); err != nil {
		t.Fatalf("tag release: %v", err)
	}
	if err := c.Tag("myapp-vnightly"); err != nil {
		t.Fatalf("tag nightly: %v", err)
	}

	tag, err := c.LatestTag("myapp")
	if err != nil {
		t.Fatalf("LatestTag() error = %v", err)
	}
	if tag != "myapp-v0.1.0" {
		t.Errorf("LatestTag() = %q, want %q", tag, "myapp-v0.1.0")
	}
}

func TestPathUnchangedSince_IdenticalTree(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: seed app")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("tag app: %v", err)
	}
	addCommit(t, c, dir, "charts/other/Chart.yaml", "feat: unrelated chart")

	unchanged, err := c.PathUnchangedSince("app-v0.1.0", "charts/app")
	if err != nil {
		t.Fatalf("PathUnchangedSince() error = %v", err)
	}
	if !unchanged {
		t.Error("PathUnchangedSince() = false, want true")
	}
}

func TestPathUnchangedSince_RealEdit(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: seed app")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("tag app: %v", err)
	}
	addCommit(t, c, dir, "charts/app/values.yaml", "feat: tune replicas")

	unchanged, err := c.PathUnchangedSince("app-v0.1.0", "charts/app")
	if err != nil {
		t.Fatalf("PathUnchangedSince() error = %v", err)
	}
	if unchanged {
		t.Error("PathUnchangedSince() = true, want false")
	}
}

func TestPathUnchangedSince_RevertedTree(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: seed app")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("tag app: %v", err)
	}
	addCommit(t, c, dir, "charts/app/values.yaml", "feat: add tuning")
	removeCommit(t, c, dir, "charts/app/values.yaml", "revert: drop tuning")

	unchanged, err := c.PathUnchangedSince("app-v0.1.0", "charts/app")
	if err != nil {
		t.Fatalf("PathUnchangedSince() error = %v", err)
	}
	if !unchanged {
		t.Error("PathUnchangedSince() = false, want true")
	}
}

func TestPathUnchangedSince_MissingPathAtTag(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/other/Chart.yaml", "feat: seed other")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("tag app: %v", err)
	}
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: introduce app")

	unchanged, err := c.PathUnchangedSince("app-v0.1.0", "charts/app")
	if err != nil {
		t.Fatalf("PathUnchangedSince() error = %v", err)
	}
	if unchanged {
		t.Error("PathUnchangedSince() = true, want false")
	}
}

func TestPathUnchangedSince_NoTag(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: seed app")

	unchanged, err := c.PathUnchangedSince("", "charts/app")
	if err != nil {
		t.Fatalf("PathUnchangedSince() error = %v", err)
	}
	if unchanged {
		t.Error("PathUnchangedSince() = true, want false")
	}
}
