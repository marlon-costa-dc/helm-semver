package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/repo/repotest"

	igit "github.com/rhysmcneill/helm-semver/internal/git"
	"github.com/rhysmcneill/helm-semver/internal/registry"
)

func TestReleaseRunner_DryRunPrintsPlanWithoutReleaseSideEffects(t *testing.T) {
	// Given a chart with a tagged release and a subsequent releasable change.
	repoRoot := t.TempDir()
	repo, err := gogit.PlainInit(repoRoot, false)
	if err != nil {
		t.Fatalf("initializing repository: %v", err)
	}
	chartDir := filepath.Join(repoRoot, "charts", "app")
	chartYAML := filepath.Join(chartDir, "Chart.yaml")
	if err := os.MkdirAll(chartDir, 0o750); err != nil {
		t.Fatalf("creating chart directory: %v", err)
	}
	if err := os.WriteFile(chartYAML, []byte("apiVersion: v2\nname: app\nversion: 0.1.0\n"), 0o600); err != nil {
		t.Fatalf("writing Chart.yaml: %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("opening worktree: %v", err)
	}
	if _, err := worktree.Add("charts/app/Chart.yaml"); err != nil {
		t.Fatalf("staging Chart.yaml: %v", err)
	}
	if _, err := worktree.Commit("feat: seed app", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com"},
	}); err != nil {
		t.Fatalf("committing chart: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("reading initial head: %v", err)
	}
	if _, err := repo.CreateTag("app-v0.1.0", head.Hash(), nil); err != nil {
		t.Fatalf("creating release tag: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chartDir, "values.yaml"), []byte("enabled: true\n"), 0o600); err != nil {
		t.Fatalf("writing chart change: %v", err)
	}
	if _, err := worktree.Add("charts/app/values.yaml"); err != nil {
		t.Fatalf("staging chart change: %v", err)
	}
	if _, err := worktree.Commit("feat: enable app", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com"},
	}); err != nil {
		t.Fatalf("committing chart change: %v", err)
	}
	headBeforeRun, err := repo.Head()
	if err != nil {
		t.Fatalf("reading head before dry-run: %v", err)
	}
	gitClient, err := igit.Open(repoRoot)
	if err != nil {
		t.Fatalf("opening git client: %v", err)
	}
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&output)

	// When dry-run release orchestration executes.
	err = (&releaseRunner{
		command:   command,
		options:   &releaseOptions{chartsDir: "charts", registry: "oci://registry.example/charts", dryRun: true},
		gitClient: gitClient,
		repoRoot:  repoRoot,
	}).run()
	if err != nil {
		t.Fatalf("running dry-run release: %v", err)
	}

	// Then it prints the release plan without changing the chart or repository.
	const wantOutput = "  app: 0.1.0 → 0.2.0 (minor)\n" +
		"    [dry-run] would push to oci://registry.example/charts\n" +
		"    [dry-run] would tag app-v0.2.0\n"
	if got := output.String(); got != wantOutput {
		t.Errorf("dry-run output = %q, want %q", got, wantOutput)
	}
	chartContents, err := os.ReadFile(chartYAML) // #nosec G304 -- the chart this test just wrote
	if err != nil {
		t.Fatalf("reading Chart.yaml after dry-run: %v", err)
	}
	if got := string(chartContents); got != "apiVersion: v2\nname: app\nversion: 0.1.0\n" {
		t.Errorf("Chart.yaml after dry-run = %q", got)
	}
	currentHead, err := repo.Head()
	if err != nil {
		t.Fatalf("reading head after dry-run: %v", err)
	}
	if currentHead.Hash() != headBeforeRun.Hash() {
		t.Errorf("dry-run created a commit: head = %s, want %s", currentHead.Hash(), headBeforeRun.Hash())
	}
}

func TestReleaseRunner_AdvancesPastAVersionTheRegistryAlreadyHolds(t *testing.T) {
	// Given a chart tagged at 0.1.0 with a feat commit since, so the derived
	// version is 0.2.0 — and a registry that already holds app 0.2.0, published
	// without its tag ever reaching this repository.
	server, err := repotest.NewOCIServer(t, t.TempDir())
	if err != nil {
		t.Fatalf("starting OCI registry: %v", err)
	}
	go server.ListenAndServe() //nolint:errcheck // stops with the test process
	publisher := &registry.OCIPublisher{
		RegistryURL: "oci://" + server.RegistryURL + "/charts",
		Username:    server.TestUsername,
		Password:    server.TestPassword,
		PlainHTTP:   true,
	}
	occupied := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(occupied, 0o750); err != nil {
		t.Fatalf("creating occupied chart: %v", err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "Chart.yaml"), []byte("apiVersion: v2\nname: app\nversion: 0.2.0\n"), 0o600); err != nil {
		t.Fatalf("writing occupied Chart.yaml: %v", err)
	}
	if err := publisher.Push(occupied, "0.2.0"); err != nil {
		t.Fatalf("publishing the occupying 0.2.0: %v", err)
	}

	repoRoot := t.TempDir()
	repo, err := gogit.PlainInit(repoRoot, false)
	if err != nil {
		t.Fatalf("initializing repository: %v", err)
	}
	chartDir := filepath.Join(repoRoot, "charts", "app")
	chartYAML := filepath.Join(chartDir, "Chart.yaml")
	if err := os.MkdirAll(chartDir, 0o750); err != nil {
		t.Fatalf("creating chart directory: %v", err)
	}
	if err := os.WriteFile(chartYAML, []byte("apiVersion: v2\nname: app\nversion: 0.1.0\n"), 0o600); err != nil {
		t.Fatalf("writing Chart.yaml: %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("opening worktree: %v", err)
	}
	signature := &object.Signature{Name: "test", Email: "test@example.com"}
	if _, err := worktree.Add("charts/app/Chart.yaml"); err != nil {
		t.Fatalf("staging Chart.yaml: %v", err)
	}
	if _, err := worktree.Commit("feat: seed app", &gogit.CommitOptions{Author: signature}); err != nil {
		t.Fatalf("committing chart: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("reading head: %v", err)
	}
	if _, err := repo.CreateTag("app-v0.1.0", head.Hash(), nil); err != nil {
		t.Fatalf("creating release tag: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chartDir, "values.yaml"), []byte("enabled: true\n"), 0o600); err != nil {
		t.Fatalf("writing chart change: %v", err)
	}
	if _, err := worktree.Add("charts/app/values.yaml"); err != nil {
		t.Fatalf("staging chart change: %v", err)
	}
	if _, err := worktree.Commit("feat: enable app", &gogit.CommitOptions{Author: signature}); err != nil {
		t.Fatalf("committing chart change: %v", err)
	}
	gitClient, err := igit.Open(repoRoot)
	if err != nil {
		t.Fatalf("opening git client: %v", err)
	}
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&output)

	// When the release runs for real (no push, no changelog).
	err = (&releaseRunner{
		command: command,
		options: &releaseOptions{
			chartsDir: "charts", registry: publisher.RegistryURL,
			authorName: "helm-semver[bot]", authorEmail: "bot@example.com",
		},
		gitClient: gitClient,
		publisher: publisher,
		repoRoot:  repoRoot,
	}).run()
	if err != nil {
		t.Fatalf("running release: %v\n%s", err, output.String())
	}

	// Then it takes the next free patch instead of the occupied one.
	if !strings.Contains(output.String(), "app: 0.2.0 is already published — advancing to 0.2.1") {
		t.Errorf("output does not report the advance:\n%s", output.String())
	}
	chartContents, err := os.ReadFile(chartYAML) // #nosec G304 -- the chart this test just wrote
	if err != nil {
		t.Fatalf("reading Chart.yaml: %v", err)
	}
	if !strings.Contains(string(chartContents), "version: 0.2.1") {
		t.Errorf("Chart.yaml = %q, want version 0.2.1", chartContents)
	}
	if _, err := repo.Tag("app-v0.2.1"); err != nil {
		t.Errorf("tag app-v0.2.1 missing: %v", err)
	}
	if _, err := repo.Tag("app-v0.2.0"); err == nil {
		t.Errorf("tag app-v0.2.0 was created over the occupied version")
	}
	published, err := publisher.PublishedVersions("app")
	if err != nil {
		t.Fatalf("listing published versions: %v", err)
	}
	if !slices.Contains(published, "0.2.1") {
		t.Errorf("registry versions = %v, want 0.2.1 published", published)
	}
}

func TestReleaseRunner_RejectsInvalidUnchangedChart(t *testing.T) {
	// Given an unchanged chart whose release metadata is invalid.
	repoRoot := t.TempDir()
	repo, err := gogit.PlainInit(repoRoot, false)
	if err != nil {
		t.Fatalf("initializing repository: %v", err)
	}
	chartDir := filepath.Join(repoRoot, "charts", "app")
	if err := os.MkdirAll(chartDir, 0o750); err != nil {
		t.Fatalf("creating chart directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("not: [valid\n"), 0o600); err != nil {
		t.Fatalf("writing Chart.yaml: %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("opening worktree: %v", err)
	}
	if _, err := worktree.Add("charts/app/Chart.yaml"); err != nil {
		t.Fatalf("staging Chart.yaml: %v", err)
	}
	if _, err := worktree.Commit("chore: add app", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com"},
	}); err != nil {
		t.Fatalf("committing chart: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("reading head: %v", err)
	}
	if _, err := repo.CreateTag("app-v0.1.0", head.Hash(), nil); err != nil {
		t.Fatalf("creating release tag: %v", err)
	}
	gitClient, err := igit.Open(repoRoot)
	if err != nil {
		t.Fatalf("opening git client: %v", err)
	}

	// When dry-run release orchestration executes.
	err = (&releaseRunner{
		command:   &cobra.Command{},
		options:   &releaseOptions{chartsDir: "charts", registry: "oci://registry.example/charts", dryRun: true},
		gitClient: gitClient,
		repoRoot:  repoRoot,
	}).run()

	// Then invalid metadata is reported even when the chart tree is unchanged.
	if err == nil || !strings.Contains(err.Error(), "loading chart app") {
		t.Errorf("release error = %v, want invalid chart metadata error", err)
	}
}
