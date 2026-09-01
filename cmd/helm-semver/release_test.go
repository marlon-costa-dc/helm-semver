package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/cobra"

	igit "github.com/rhysmcneill/helm-semver/internal/git"
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
	chartContents, err := os.ReadFile(chartYAML)
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
