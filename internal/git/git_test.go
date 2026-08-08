package git

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// initTestRepo creates a temporary git repo with an initial commit and returns
// the Client and the repo root path.
func initTestRepo(t *testing.T) (*Client, string) {
	t.Helper()
	dir := t.TempDir()

	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}

	wt, _ := repo.Worktree()

	// Seed with an initial file and commit.
	seedFile := filepath.Join(dir, "README.md")
	_ = os.WriteFile(seedFile, []byte("# test"), 0o600)
	_, _ = wt.Add("README.md")
	_, err = wt.Commit("chore: initial commit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("initial commit: %v", err)
	}

	return &Client{repo: repo}, dir
}

func addCommit(t *testing.T, c *Client, dir, file, msg string) {
	t.Helper()
	wt, _ := c.repo.Worktree()
	fullPath := filepath.Join(dir, file)
	_ = os.MkdirAll(filepath.Dir(fullPath), 0o750)
	_ = os.WriteFile(fullPath, []byte(msg), 0o600)
	_, _ = wt.Add(file)
	_, err := wt.Commit(msg, &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("commit %q: %v", msg, err)
	}
}

func removeCommit(t *testing.T, c *Client, dir, file, msg string) {
	t.Helper()
	wt, _ := c.repo.Worktree()
	if err := os.Remove(filepath.Join(dir, file)); err != nil {
		t.Fatalf("remove %q: %v", file, err)
	}
	_, _ = wt.Add(file)
	_, err := wt.Commit(msg, &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("commit %q: %v", msg, err)
	}
}

func TestLatestTag_NoTags(t *testing.T) {
	c, _ := initTestRepo(t)
	tag, err := c.LatestTag("myapp")
	if err != nil {
		t.Fatalf("LatestTag() error = %v", err)
	}
	if tag != "" {
		t.Errorf("LatestTag() = %q, want empty", tag)
	}
}

func TestLatestTag_ReturnsLatest(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/myapp/Chart.yaml", "fix: patch 1")

	if err := c.Tag("myapp-v0.1.0"); err != nil {
		t.Fatalf("Tag() error = %v", err)
	}

	addCommit(t, c, dir, "charts/myapp/Chart.yaml", "feat: minor 1")
	if err := c.Tag("myapp-v0.2.0"); err != nil {
		t.Fatalf("Tag() error = %v", err)
	}

	tag, err := c.LatestTag("myapp")
	if err != nil {
		t.Fatalf("LatestTag() error = %v", err)
	}
	if tag != "myapp-v0.2.0" {
		t.Errorf("LatestTag() = %q, want %q", tag, "myapp-v0.2.0")
	}
}

func TestCommitsSince_NoTag(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/app/values.yaml", "feat: add redis")
	addCommit(t, c, dir, "charts/app/Chart.yaml", "fix: bump version")

	commits, err := c.CommitsSince("", "charts/app")
	if err != nil {
		t.Fatalf("CommitsSince() error = %v", err)
	}
	if len(commits) < 2 {
		t.Errorf("CommitsSince() returned %d commits, want >= 2", len(commits))
	}
	for _, ci := range commits {
		if ci.Subject == "" {
			t.Error("CommitInfo.Subject should not be empty")
		}
		if ci.Hash == "" {
			t.Error("CommitInfo.Hash should not be empty")
		}
		if len(ci.Hash) != 40 {
			t.Errorf("CommitInfo.Hash should be 40 chars, got %d: %s", len(ci.Hash), ci.Hash)
		}
	}
}

func TestParsePR(t *testing.T) {
	tests := []struct {
		subject string
		want    int
	}{
		{"feat: add redis (#42)", 42},
		{"fix: auth error (#123)", 123},
		{"chore: cleanup", 0},
		{"feat: no trailing paren (#", 0},
	}
	for _, tt := range tests {
		got := parsePR(tt.subject)
		if got != tt.want {
			t.Errorf("parsePR(%q) = %d, want %d", tt.subject, got, tt.want)
		}
	}
}

func TestSubjects(t *testing.T) {
	commits := []CommitInfo{
		{Subject: "feat: a", Hash: "abc123", PR: 1},
		{Subject: "fix: b", Hash: "def456", PR: 0},
	}
	got := Subjects(commits)
	want := []string{"feat: a", "fix: b"}
	if len(got) != len(want) {
		t.Fatalf("Subjects() len = %d, want %d", len(got), len(want))
	}
	for i, s := range got {
		if s != want[i] {
			t.Errorf("Subjects()[%d] = %q, want %q", i, s, want[i])
		}
	}
}

func TestStageAndCommit(t *testing.T) {
	c, dir := initTestRepo(t)

	newFile := filepath.Join(dir, "charts/app/Chart.yaml")
	_ = os.MkdirAll(filepath.Dir(newFile), 0o750)
	_ = os.WriteFile(newFile, []byte("version: 0.2.0"), 0o600)

	if err := c.StageFile("charts/app/Chart.yaml"); err != nil {
		t.Fatalf("StageFile() error = %v", err)
	}

	if err := c.Commit("chore(app): release v0.2.0 [skip ci]", "bot", "bot@bot.com"); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	head, _ := c.repo.Head()
	commit, _ := c.repo.CommitObject(head.Hash())
	if commit.Message != "chore(app): release v0.2.0 [skip ci]" {
		t.Errorf("commit message = %q", commit.Message)
	}
}

func TestCommit_TimestampIsRecent(t *testing.T) {
	c, dir := initTestRepo(t)

	newFile := filepath.Join(dir, "foo.txt")
	_ = os.WriteFile(newFile, []byte("x"), 0o600)
	_ = c.StageFile("foo.txt")

	before := time.Now().Add(-time.Second)
	if err := c.Commit("test: check timestamp", "bot", "bot@example.com"); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	after := time.Now().Add(time.Second)

	head, _ := c.repo.Head()
	commit, _ := c.repo.CommitObject(head.Hash())

	if commit.Author.When.Before(before) || commit.Author.When.After(after) {
		t.Errorf("commit timestamp %v outside expected range [%v, %v]",
			commit.Author.When, before, after)
	}
}

func TestRemoteIsHTTPS(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"https", "https://github.com/user/repo.git", true},
		{"http", "http://localhost/repo.git", true},
		{"ssh scp-style", "git@github.com:user/repo.git", false},
		{"ssh url", "ssh://git@github.com/user/repo.git", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := initTestRepo(t)
			_, err := c.repo.CreateRemote(&config.RemoteConfig{
				Name: "testremote",
				URLs: []string{tt.url},
			})
			if err != nil {
				t.Fatalf("create remote: %v", err)
			}
			got := c.remoteIsHTTPS("testremote")
			if got != tt.want {
				t.Errorf("remoteIsHTTPS(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestRemoteIsHTTPS_UnknownRemote(t *testing.T) {
	c, _ := initTestRepo(t)
	// Non-existent remote should default to true (assume HTTPS).
	if !c.remoteIsHTTPS("nonexistent") {
		t.Error("remoteIsHTTPS(nonexistent) = false, want true")
	}
}

func TestPush_LocalBareRemote(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: something")

	bareDir := t.TempDir()
	if _, err := gogit.PlainInit(bareDir, true); err != nil {
		t.Fatalf("init bare repo: %v", err)
	}

	if _, err := c.repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{bareDir},
	}); err != nil {
		t.Fatalf("create remote: %v", err)
	}

	// Push with no token to a local file remote — should succeed without auth.
	if err := c.Push("origin", ""); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
}

// tagCommitOutsidePathFilter reproduces the production bootstrap shape that
// caused every chart to re-release on every run: the chart's baseline tag
// points at a commit that does NOT touch the chart's own directory (in
// cosmos-charts, tag cosmos-clickhouse-v0.4.120 -> commit 79242118cd, which
// touched only .github/workflows/chart-gates.yml).
//
// CommitsSince must implement `git log <tag>..HEAD -- <path>` semantics: every
// commit reachable from the tag is excluded by ANCESTRY, regardless of whether
// that commit survives the path filter. Stopping on an exact hash match inside
// an already-path-filtered stream cannot work here, because the tagged commit
// never appears in that stream.
func TestCommitsSince_ExcludesTagAncestry_WhenTagCommitOutsidePathFilter(t *testing.T) {
	c, dir := initTestRepo(t)

	// Released history for the chart.
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: add app chart")

	// A commit that does NOT touch charts/app — this is what gets tagged.
	addCommit(t, c, dir, ".github/workflows/gates.yml", "fix(ci): use action")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("Tag() error = %v", err)
	}

	// The only genuinely new work for the chart after the tag.
	addCommit(t, c, dir, "charts/app/values.yaml", "chore: retune limits")

	commits, err := c.CommitsSince("app-v0.1.0", "charts/app")
	if err != nil {
		t.Fatalf("CommitsSince() error = %v", err)
	}

	subjects := Subjects(commits)
	if len(subjects) != 1 || subjects[0] != "chore: retune limits" {
		t.Errorf("CommitsSince() = %v, want exactly [\"chore: retune limits\"]; "+
			"commits already released before app-v0.1.0 leaked back into the range", subjects)
	}

	for _, s := range subjects {
		if s == "feat: add app chart" {
			t.Error("pre-tag commit \"feat: add app chart\" was re-included: this is the " +
				"cascade defect — a released feat: commit forces a perpetual minor bump")
		}
	}
}

// TestCommitsSince_PathFilterRespectsDirectoryBoundary pins that a chart name
// which is a string prefix of another chart name does not absorb its sibling's
// commits. strings.HasPrefix(path, "charts/cosmos-vault") also matches
// "charts/cosmos-vault-extras/Chart.yaml".
func TestCommitsSince_PathFilterRespectsDirectoryBoundary(t *testing.T) {
	c, dir := initTestRepo(t)

	addCommit(t, c, dir, "charts/vault/Chart.yaml", "feat: vault only")
	addCommit(t, c, dir, "charts/vault-extras/Chart.yaml", "feat: extras only")

	commits, err := c.CommitsSince("", "charts/vault")
	if err != nil {
		t.Fatalf("CommitsSince() error = %v", err)
	}

	for _, s := range Subjects(commits) {
		if s == "feat: extras only" {
			t.Errorf("CommitsSince(_, %q) absorbed a commit from the sibling chart "+
				"charts/vault-extras; got %v", "charts/vault", Subjects(commits))
		}
	}
}

// TestLatestTag_OrdersBySemverNotLexicographically pins that the baseline tag
// is the highest SEMANTIC version. Lexicographically "v0.4.99" > "v0.4.120",
// so string sorting silently selects a stale baseline once a chart passes
// patch .99 — every cosmos-charts chart is in the .11x-.17x range today.
func TestLatestTag_OrdersBySemverNotLexicographically(t *testing.T) {
	c, dir := initTestRepo(t)

	for _, tag := range []string{"myapp-v0.4.9", "myapp-v0.4.99", "myapp-v0.4.120"} {
		addCommit(t, c, dir, "charts/myapp/Chart.yaml", "chore: release "+tag)
		if err := c.Tag(tag); err != nil {
			t.Fatalf("Tag(%q) error = %v", tag, err)
		}
	}

	tag, err := c.LatestTag("myapp")
	if err != nil {
		t.Fatalf("LatestTag() error = %v", err)
	}
	if tag != "myapp-v0.4.120" {
		t.Errorf("LatestTag() = %q, want %q (lexicographic sort ranks v0.4.99 above v0.4.120)",
			tag, "myapp-v0.4.120")
	}
}

// TestLatestTag_IgnoresUnparseableTags pins that a non-semver tag sharing the
// chart prefix cannot become the baseline.
func TestLatestTag_IgnoresUnparseableTags(t *testing.T) {
	c, dir := initTestRepo(t)

	addCommit(t, c, dir, "charts/myapp/Chart.yaml", "feat: initial")
	if err := c.Tag("myapp-v0.1.0"); err != nil {
		t.Fatalf("Tag() error = %v", err)
	}
	if err := c.Tag("myapp-vnightly"); err != nil {
		t.Fatalf("Tag() error = %v", err)
	}

	tag, err := c.LatestTag("myapp")
	if err != nil {
		t.Fatalf("LatestTag() error = %v", err)
	}
	if tag != "myapp-v0.1.0" {
		t.Errorf("LatestTag() = %q, want %q", tag, "myapp-v0.1.0")
	}
}

// TestPathUnchangedSince_IdenticalTreeNeedsNoWalk pins the cheap gate: when a
// chart's directory tree is byte-identical between its baseline tag and HEAD,
// nothing in that chart changed and no commit walk is needed to know it.
//
// This is the difference between resolving two tree objects and diffing every
// commit in history. In cosmos-charts the full-history walk does not finish in
// 900s for a single chart, while comparing tree hashes decides all 79 charts in
// 0.83s.
func TestPathUnchangedSince_IdenticalTreeNeedsNoWalk(t *testing.T) {
	c, dir := initTestRepo(t)

	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: seed app")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("Tag() error = %v", err)
	}
	// Work lands elsewhere in the repository, never inside charts/app.
	addCommit(t, c, dir, "charts/other/Chart.yaml", "feat: unrelated chart")

	unchanged, err := c.PathUnchangedSince("app-v0.1.0", "charts/app")
	if err != nil {
		t.Fatalf("PathUnchangedSince() error = %v", err)
	}
	if !unchanged {
		t.Error("charts/app is byte-identical to its tag, so it must report unchanged")
	}
}

// TestPathUnchangedSince_RealEditIsDetected is the regression guard: the gate
// must never suppress a chart that genuinely changed.
func TestPathUnchangedSince_RealEditIsDetected(t *testing.T) {
	c, dir := initTestRepo(t)

	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: seed app")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("Tag() error = %v", err)
	}
	addCommit(t, c, dir, "charts/app/values.yaml", "feat: tune replicas")

	unchanged, err := c.PathUnchangedSince("app-v0.1.0", "charts/app")
	if err != nil {
		t.Fatalf("PathUnchangedSince() error = %v", err)
	}
	if unchanged {
		t.Error("charts/app gained a file after its tag, so it must report changed")
	}
}

// TestPathUnchangedSince_RevertToTaggedTreeIsUnchanged pins the boundary the
// gate deliberately draws. Commits landed and were then reverted, so the tree
// is identical to the tagged one again. There is nothing to publish: the
// artifact built from HEAD would be byte-identical to the one the tag already
// released, and releasing it would only burn a version number.
func TestPathUnchangedSince_RevertToTaggedTreeIsUnchanged(t *testing.T) {
	c, dir := initTestRepo(t)

	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: seed app")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("Tag() error = %v", err)
	}
	addCommit(t, c, dir, "charts/app/values.yaml", "feat: add tuning")
	removeCommit(t, c, dir, "charts/app/values.yaml", "revert: drop tuning")

	unchanged, err := c.PathUnchangedSince("app-v0.1.0", "charts/app")
	if err != nil {
		t.Fatalf("PathUnchangedSince() error = %v", err)
	}
	if !unchanged {
		t.Error("a reverted tree matches the tag byte-for-byte and must report unchanged")
	}
}

// TestPathUnchangedSince_MissingPathAtTagIsChanged covers a chart added after
// its baseline tag was cut: the path does not exist at the tag, so it changed.
func TestPathUnchangedSince_MissingPathAtTagIsChanged(t *testing.T) {
	c, dir := initTestRepo(t)

	addCommit(t, c, dir, "charts/other/Chart.yaml", "feat: seed other")
	if err := c.Tag("app-v0.1.0"); err != nil {
		t.Fatalf("Tag() error = %v", err)
	}
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: introduce app")

	unchanged, err := c.PathUnchangedSince("app-v0.1.0", "charts/app")
	if err != nil {
		t.Fatalf("PathUnchangedSince() error = %v", err)
	}
	if unchanged {
		t.Error("a chart absent at its tag must report changed")
	}
}

// TestPathUnchangedSince_NoTagIsChanged covers the first release: with no
// baseline there is nothing to compare, so the chart must be considered
// changed and fall through to the normal commit analysis.
func TestPathUnchangedSince_NoTagIsChanged(t *testing.T) {
	c, dir := initTestRepo(t)
	addCommit(t, c, dir, "charts/app/Chart.yaml", "feat: seed app")

	unchanged, err := c.PathUnchangedSince("", "charts/app")
	if err != nil {
		t.Fatalf("PathUnchangedSince() error = %v", err)
	}
	if unchanged {
		t.Error("a chart with no baseline tag must report changed")
	}
}
