package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/rhysmcneill/helm-semver/internal/changelog"
	"github.com/rhysmcneill/helm-semver/internal/chart"
	igit "github.com/rhysmcneill/helm-semver/internal/git"
	"github.com/rhysmcneill/helm-semver/internal/registry"
	"github.com/rhysmcneill/helm-semver/internal/release"
	"github.com/rhysmcneill/helm-semver/internal/semver"
)

type releaseRunner struct {
	command   *cobra.Command
	options   *releaseOptions
	gitClient *igit.Client
	publisher registry.Publisher
	repoRoot  string
}

type chartRelease struct {
	name    string
	dir     string
	relPath string
	lastTag string
	commits []igit.CommitInfo
}

func (runner *releaseRunner) run() error {
	candidates, err := runner.findCandidates()
	if err != nil {
		return err
	}
	if err := runner.collectCommits(candidates); err != nil {
		return err
	}

	released := 0
	for _, candidate := range candidates {
		if err := runner.releaseChart(candidate); err != nil {
			return err
		}
		released++
	}
	if released > 0 && runner.options.gitPush && !runner.options.dryRun {
		_, _ = fmt.Fprintln(runner.command.OutOrStdout(), "Pushing commits and tags…")
		if err := runner.gitClient.Push("origin", runner.options.gitToken); err != nil {
			return fmt.Errorf("git push: %w", err)
		}
	}
	return nil
}

func (runner *releaseRunner) findCandidates() ([]chartRelease, error) {
	chartsDir := filepath.Join(runner.repoRoot, runner.options.chartsDir)
	entries, err := os.ReadDir(chartsDir)
	if err != nil {
		return nil, fmt.Errorf("reading charts dir %s: %w", chartsDir, err)
	}
	candidates := make([]chartRelease, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate, ok, err := runner.findCandidate(chartsDir, entry.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

func (runner *releaseRunner) findCandidate(chartsDir, chartName string) (chartRelease, bool, error) {
	chartDir := filepath.Join(chartsDir, chartName)
	chartYAML := filepath.Join(chartDir, "Chart.yaml")
	if _, err := os.Stat(chartYAML); os.IsNotExist(err) {
		return chartRelease{}, false, nil
	}
	if _, err := chart.Load(chartYAML); err != nil {
		return chartRelease{}, false, fmt.Errorf("loading chart %s: %w", chartName, err)
	}
	lastTag, err := runner.gitClient.LatestTag(runner.options.tagPrefix + chartName)
	if err != nil {
		return chartRelease{}, false, fmt.Errorf("resolving latest tag for %s: %w", chartName, err)
	}
	relPath, err := filepath.Rel(runner.repoRoot, chartDir)
	if err != nil {
		return chartRelease{}, false, fmt.Errorf("resolving relative path for %s: %w", chartName, err)
	}
	unchanged, err := runner.gitClient.PathUnchangedSince(lastTag, relPath)
	if err != nil {
		return chartRelease{}, false, fmt.Errorf("comparing %s against %s: %w", chartName, lastTag, err)
	}
	if unchanged {
		_, _ = fmt.Fprintf(runner.command.OutOrStdout(), "  %s: unchanged since %s — skipping\n", chartName, lastTag)
		return chartRelease{}, false, nil
	}
	return chartRelease{name: chartName, dir: chartDir, relPath: relPath, lastTag: lastTag}, true, nil
}

func (runner *releaseRunner) collectCommits(candidates []chartRelease) error {
	ranges := make([]igit.ChartRange, 0, len(candidates))
	for _, candidate := range candidates {
		ranges = append(ranges, igit.ChartRange{Chart: candidate.name, Tag: candidate.lastTag, Path: candidate.relPath})
	}
	batch, err := runner.gitClient.CommitsSinceBatch(ranges)
	if err != nil {
		return fmt.Errorf("reading commits for the changed charts: %w", err)
	}
	for index := range candidates {
		candidates[index].commits = batch[candidates[index].name]
	}
	return nil
}

func (runner *releaseRunner) releaseChart(candidate chartRelease) error {
	out := runner.command.OutOrStdout()
	metadata, err := chart.Load(filepath.Join(candidate.dir, "Chart.yaml"))
	if err != nil {
		return fmt.Errorf("loading chart %s: %w", candidate.name, err)
	}
	bump := semver.Analyze(igit.Subjects(candidate.commits))
	if bump == semver.BumpNone {
		_, _ = fmt.Fprintf(out, "  %s: no releasable commits — skipping\n", candidate.name)
		return nil
	}
	newVersion, err := semver.Next(metadata.Version, bump)
	if err != nil {
		return fmt.Errorf("computing next version for %s: %w", candidate.name, err)
	}
	newTag := runner.options.tagPrefix + candidate.name + "-v" + newVersion
	_, _ = fmt.Fprintf(out, "  %s: %s → %s (%s)\n", candidate.name, metadata.Version, newVersion, bump)
	if runner.options.dryRun {
		_, _ = fmt.Fprintf(out, "    [dry-run] would push to %s\n", runner.options.registry)
		_, _ = fmt.Fprintf(out, "    [dry-run] would tag %s\n", newTag)
		if runner.options.changelog {
			_, _ = fmt.Fprintln(out, "    [dry-run] would update CHANGELOG.md")
		}
		if runner.options.githubRelease {
			_, _ = fmt.Fprintf(out, "    [dry-run] would create GitHub Release %s\n", newTag)
		}
		return nil
	}
	if err := chart.BumpVersion(filepath.Join(candidate.dir, "Chart.yaml"), newVersion); err != nil {
		return fmt.Errorf("bumping version for %s: %w", candidate.name, err)
	}
	if runner.options.dependencyBuild {
		if err := chart.BuildDependencies(candidate.dir, out); err != nil {
			return fmt.Errorf("building dependencies for %s: %w", candidate.name, err)
		}
	}
	if err := runner.publisher.Push(candidate.dir, newVersion); err != nil {
		return fmt.Errorf("pushing %s: %w", candidate.name, err)
	}
	_, _ = fmt.Fprintf(out, "    pushed to %s\n", runner.options.registry)
	released := []string{filepath.Join(candidate.relPath, "Chart.yaml")}
	changelogPath, err := runner.writeChangelog(candidate, newVersion, newTag)
	if err != nil {
		return err
	}
	if changelogPath != "" {
		released = append(released, changelogPath)
	}
	if err := runner.gitClient.Commit(
		fmt.Sprintf("chore(%s): release v%s [skip ci]", candidate.name, newVersion),
		runner.options.authorName, runner.options.authorEmail, released...,
	); err != nil {
		return fmt.Errorf("committing release for %s: %w", candidate.name, err)
	}
	if err := runner.gitClient.Tag(newTag); err != nil {
		return fmt.Errorf("tagging %s: %w", newTag, err)
	}
	_, _ = fmt.Fprintf(out, "    tagged %s\n", newTag)
	return runner.createGitHubRelease(candidate, newVersion, newTag)
}

// writeChangelog appends this release's entry and returns the path it wrote,
// or an empty path when changelog generation is disabled.
func (runner *releaseRunner) writeChangelog(candidate chartRelease, newVersion, newTag string) (string, error) {
	if !runner.options.changelog {
		return "", nil
	}
	path := filepath.Join(candidate.dir, "CHANGELOG.md")
	repo := changelog.RepoInfo{Owner: runner.options.githubOwner, Name: runner.options.githubRepo}
	if err := changelog.Append(path, newVersion, time.Now(), candidate.commits, candidate.lastTag, newTag, repo); err != nil {
		return "", fmt.Errorf("updating changelog: %w", err)
	}
	return filepath.Join(candidate.relPath, "CHANGELOG.md"), nil
}

func (runner *releaseRunner) createGitHubRelease(candidate chartRelease, newVersion, newTag string) error {
	if !runner.options.githubRelease || runner.options.gitToken == "" {
		return nil
	}
	notes := release.BuildReleaseNotes(candidate.commits, runner.options.githubOwner, runner.options.githubRepo)
	client := release.New(runner.options.gitToken, runner.options.githubOwner, runner.options.githubRepo)
	url, err := client.CreateRelease(context.Background(), newTag, candidate.name+" "+newVersion, notes)
	if err != nil {
		return fmt.Errorf("creating GitHub release for %s: %w", newTag, err)
	}
	_, _ = fmt.Fprintf(runner.command.OutOrStdout(), "    GitHub Release: %s\n", url)
	return nil
}
