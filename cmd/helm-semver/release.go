package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/rhysmcneill/helm-semver/internal/catalog"
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
	// newest memoizes the newest published version per chart within this run.
	newest map[string]string
}

type chartRelease struct {
	name    string
	dir     string
	relPath string
	lastTag string
	commits []igit.CommitInfo
	// internal names the dependencies this release owns (see internalDependencies).
	internal []string
}

// plannedRelease is one chart the run will release, with the version decided
// before anything is published.
type plannedRelease struct {
	chartRelease
	currentVersion string
	version        string
	bump           semver.BumpType
	tag            string
	// pins is the version each internal dependency adopts before validation.
	pins map[string]string
}

// planEntry is the machine-readable form of a planned release (--output json).
type planEntry struct {
	Chart          string            `json:"chart"`
	Path           string            `json:"path"`
	LastTag        string            `json:"lastTag"`
	CurrentVersion string            `json:"currentVersion"`
	Version        string            `json:"version"`
	Bump           string            `json:"bump"`
	Tag            string            `json:"tag"`
	Pins           map[string]string `json:"pins,omitempty"`
}

// run decides every release first, asks the registry whether it accepts each
// chart, and only then publishes chart by chart — each pushed to the git remote
// as soon as it is in the registry. A refusal therefore costs nothing, and a
// later failure leaves every earlier release published and tagged.
func (runner *releaseRunner) run() error {
	if runner.newest == nil {
		runner.newest = map[string]string{}
	}
	if runner.options.catalog != "" {
		if runner.options.githubOwner == "" || runner.options.githubRepo == "" {
			return fmt.Errorf("the catalog receipt names the source repository: set --github-owner and --github-repo")
		}
		if _, ok := runner.publisher.(registry.DigestRecorder); !ok {
			return fmt.Errorf("--catalog needs a backend that answers the pushed digest; %s does not", runner.options.registryType)
		}
	}
	candidates, err := runner.findCandidates()
	if err != nil {
		return err
	}
	if err := runner.collectCommits(candidates); err != nil {
		return err
	}
	plan, err := runner.plan(candidates)
	if err != nil {
		return err
	}
	if !runner.options.dryRun {
		if err := runner.preflight(plan); err != nil {
			return err
		}
		for _, planned := range plan {
			if err := runner.releaseChart(planned); err != nil {
				return err
			}
		}
	}
	if runner.options.output == outputJSON {
		return runner.writePlan(plan)
	}
	return nil
}

// progress is where human-readable progress goes: stdout, unless stdout
// carries the JSON plan.
func (runner *releaseRunner) progress() io.Writer {
	if runner.options.output == outputJSON {
		return runner.command.ErrOrStderr()
	}
	return runner.command.OutOrStdout()
}

func (runner *releaseRunner) findCandidates() ([]chartRelease, error) {
	chartsDir := filepath.Join(runner.repoRoot, runner.options.chartsDir)
	entries, err := os.ReadDir(chartsDir)
	if err != nil {
		return nil, fmt.Errorf("reading charts dir %s: %w", chartsDir, err)
	}
	// found records, for each name --charts gives, whether it is a chart here.
	found := make(map[string]bool, len(runner.options.charts))
	for _, name := range runner.options.charts {
		found[name] = false
	}
	candidates := make([]chartRelease, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, named := found[entry.Name()]; len(runner.options.charts) > 0 && !named {
			continue
		}
		if _, err := os.Stat(filepath.Join(chartsDir, entry.Name(), "Chart.yaml")); err == nil {
			found[entry.Name()] = true
		}
		candidate, ok, err := runner.findCandidate(chartsDir, entry.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			candidates = append(candidates, candidate)
		}
	}
	for _, name := range runner.options.charts {
		if !found[name] {
			return nil, fmt.Errorf("--charts names %q, which is not a chart in %s", name, chartsDir)
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
		_, _ = fmt.Fprintf(runner.progress(), "  %s: unchanged since %s — skipping\n", chartName, lastTag)
		return chartRelease{}, false, nil
	}
	internal, err := runner.internalDependencies(chartDir)
	if err != nil {
		return chartRelease{}, false, fmt.Errorf("reading dependencies of %s: %w", chartName, err)
	}
	return chartRelease{name: chartName, dir: chartDir, relPath: relPath, lastTag: lastTag, internal: internal}, true, nil
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

// plan decides the version of every changed chart and the parent versions it
// adopts. A chart whose tree differs from its last tag is changed: its commits
// decide the bump, and a change no conventional commit names is still a patch.
// The dry run asks the registry exactly as the release does, so the plan it
// prints is the plan the release executes.
func (runner *releaseRunner) plan(candidates []chartRelease) ([]plannedRelease, error) {
	out := runner.progress()
	plan := make([]plannedRelease, 0, len(candidates))
	for _, candidate := range candidates {
		metadata, err := chart.Load(filepath.Join(candidate.dir, "Chart.yaml"))
		if err != nil {
			return nil, fmt.Errorf("loading chart %s: %w", candidate.name, err)
		}
		bump := semver.Analyze(igit.Subjects(candidate.commits))
		if bump == semver.BumpNone {
			bump = semver.BumpPatch
		}
		version, err := semver.Next(metadata.Version, bump)
		if err != nil {
			return nil, fmt.Errorf("computing next version for %s: %w", candidate.name, err)
		}
		version, err = runner.nextFreeVersion(metadata.Name, version)
		if err != nil {
			return nil, err
		}
		planned := plannedRelease{
			chartRelease: candidate, currentVersion: metadata.Version, version: version, bump: bump,
			tag: runner.options.tagPrefix + candidate.name + "-v" + version,
		}
		_, _ = fmt.Fprintf(out, "  %s: %s → %s (%s)\n", candidate.name, metadata.Version, version, bump)
		if runner.options.dryRun && runner.options.output != outputJSON {
			_, _ = fmt.Fprintf(out, "    [dry-run] would push to %s\n", runner.options.registry)
			_, _ = fmt.Fprintf(out, "    [dry-run] would tag %s\n", planned.tag)
			if runner.options.changelog {
				_, _ = fmt.Fprintln(out, "    [dry-run] would update CHANGELOG.md")
			}
			if runner.options.githubRelease {
				_, _ = fmt.Fprintf(out, "    [dry-run] would create GitHub Release %s\n", planned.tag)
			}
		}
		plan = append(plan, planned)
	}
	ordered, err := orderPlan(plan)
	if err != nil {
		return nil, err
	}
	plannedVersions := make(map[string]string, len(ordered))
	for _, planned := range ordered {
		plannedVersions[planned.name] = planned.version
	}
	for index := range ordered {
		pins, err := runner.pinsFor(ordered[index], plannedVersions)
		if err != nil {
			return nil, err
		}
		ordered[index].pins = pins
	}
	return ordered, nil
}

// preflight asks the registry, before the first push, whether it accepts every
// planned chart. Backends that cannot answer are not asked.
func (runner *releaseRunner) preflight(plan []plannedRelease) error {
	checker, ok := runner.publisher.(registry.PushChecker)
	if !ok || len(plan) == 0 {
		return nil
	}
	for _, planned := range plan {
		if err := checker.CheckPush(planned.name); err != nil {
			return fmt.Errorf("preflight, nothing published: %w", err)
		}
	}
	_, _ = fmt.Fprintf(runner.progress(), "  preflight: %s accepts all %d chart(s)\n", runner.options.registry, len(plan))
	return nil
}

// writePlan prints the planned (dry run) or published (real run) releases.
func (runner *releaseRunner) writePlan(plan []plannedRelease) error {
	entries := make([]planEntry, 0, len(plan))
	for _, planned := range plan {
		entries = append(entries, planEntry{
			Chart: planned.name, Path: filepath.ToSlash(planned.relPath), LastTag: planned.lastTag,
			CurrentVersion: planned.currentVersion, Version: planned.version,
			Bump: planned.bump.String(), Tag: planned.tag, Pins: planned.pins,
		})
	}
	encoder := json.NewEncoder(runner.command.OutOrStdout())
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(map[string][]planEntry{"releases": entries}); err != nil {
		return fmt.Errorf("writing the release plan: %w", err)
	}
	return nil
}

// releaseChart publishes one chart as one transaction: adopt the parent pins,
// validate, bump, push, commit, tag, push and record in the catalog. Until the
// push succeeds any failure restores Chart.yaml, so no pin reaches the branch
// without the release that validated it.
func (runner *releaseRunner) releaseChart(planned plannedRelease) error {
	out := runner.progress()
	chartYAML := filepath.Join(planned.dir, "Chart.yaml")
	original, err := os.ReadFile(chartYAML) // #nosec
	if err != nil {
		return fmt.Errorf("reading %s: %w", chartYAML, err)
	}
	restore := func(cause error) error {
		if err := os.WriteFile(chartYAML, original, 0o644); err != nil { // #nosec
			return errors.Join(cause, fmt.Errorf("restoring %s: %w", chartYAML, err))
		}
		return cause
	}
	for parent, version := range planned.pins {
		if err := runner.confirmPublished(parent, version); err != nil {
			return fmt.Errorf("%s: %w", planned.name, err)
		}
	}
	if len(planned.pins) > 0 {
		if _, err := chart.SetDependencyVersions(chartYAML, planned.pins); err != nil {
			return restore(fmt.Errorf("pinning parents of %s: %w", planned.name, err))
		}
	}
	if err := runner.validate(planned, out); err != nil {
		return restore(err)
	}
	if err := chart.BumpVersion(chartYAML, planned.version); err != nil {
		return restore(fmt.Errorf("bumping version for %s: %w", planned.name, err))
	}
	if err := runner.publish(planned, out); err != nil {
		return restore(err)
	}
	_, _ = fmt.Fprintf(out, "    pushed to %s\n", runner.options.registry)
	released := []string{filepath.Join(planned.relPath, "Chart.yaml")}
	changelogPath, err := runner.writeChangelog(planned.chartRelease, planned.version, planned.tag)
	if err != nil {
		return err
	}
	if changelogPath != "" {
		released = append(released, changelogPath)
	}
	if err := runner.gitClient.Commit(
		fmt.Sprintf("chore(%s): release v%s [skip ci]", planned.name, planned.version),
		runner.options.authorName, runner.options.authorEmail, released...,
	); err != nil {
		return fmt.Errorf("committing release for %s: %w", planned.name, err)
	}
	if err := runner.gitClient.Tag(planned.tag); err != nil {
		return fmt.Errorf("tagging %s: %w", planned.tag, err)
	}
	_, _ = fmt.Fprintf(out, "    tagged %s\n", planned.tag)
	if runner.options.gitPush {
		if err := runner.gitClient.PushRelease("origin", runner.options.gitToken, planned.tag); err != nil {
			return fmt.Errorf("git push: %w", err)
		}
		_, _ = fmt.Fprintf(out, "    pushed the release commit and %s\n", planned.tag)
	}
	if err := runner.recordCatalog(planned, out); err != nil {
		return err
	}
	return runner.createGitHubRelease(planned.chartRelease, planned.version, planned.tag)
}

// validate runs the project's gate for the chart after its parents are pinned
// and before anything is published. The chart travels in the environment
// (HELM_SEMVER_CHART, HELM_SEMVER_CHART_DIR); a non-zero exit stops the run.
func (runner *releaseRunner) validate(planned plannedRelease, out io.Writer) error {
	if runner.options.validateCmd == "" {
		return nil
	}
	command := exec.Command("sh", "-c", runner.options.validateCmd) // #nosec // operator-declared gate
	command.Dir = runner.repoRoot
	command.Env = append(os.Environ(),
		"HELM_SEMVER_CHART="+planned.name,
		"HELM_SEMVER_CHART_DIR="+planned.dir,
	)
	command.Stdout = out
	command.Stderr = runner.command.ErrOrStderr()
	if err := command.Run(); err != nil {
		return fmt.Errorf("validating %s before release: %w", planned.name, err)
	}
	_, _ = fmt.Fprintf(out, "    validated %s against its pinned parents\n", planned.name)
	return nil
}

// recordCatalog writes the published version, the digest the registry
// confirmed and the release commit into the channel catalog.
func (runner *releaseRunner) recordCatalog(planned plannedRelease, out io.Writer) error {
	if runner.options.catalog == "" {
		return nil
	}
	recorder := runner.publisher.(registry.DigestRecorder)
	digest, err := recorder.PushedDigest(planned.name, planned.version)
	if err != nil {
		return fmt.Errorf("recording %s in the catalog: %w", planned.name, err)
	}
	commit, err := runner.gitClient.HeadHash()
	if err != nil {
		return fmt.Errorf("recording %s in the catalog: %w", planned.name, err)
	}
	entry := catalog.Entry{
		Version: planned.version, Digest: digest,
		Repo: runner.options.githubOwner + "/" + runner.options.githubRepo, Commit: commit,
	}
	if err := catalog.Record(runner.options.catalog, planned.name, entry); err != nil {
		return fmt.Errorf("recording %s in the catalog: %w", planned.name, err)
	}
	_, _ = fmt.Fprintf(out, "    recorded %s %s in %s\n", planned.name, planned.version, runner.options.catalog)
	return nil
}

// publish packages and pushes one chart. The dependencies it builds for the
// package are removed from the source chart afterwards, pushed or not, so the
// next chart and the release commit see the tree as the repository has it.
func (runner *releaseRunner) publish(planned plannedRelease, out io.Writer) (err error) {
	if runner.options.dependencyBuild {
		removeBuilt, buildErr := chart.BuildDependencies(planned.dir, out)
		if buildErr != nil {
			return fmt.Errorf("building dependencies for %s: %w", planned.name, buildErr)
		}
		defer func() { err = errors.Join(err, removeBuilt()) }()
	}
	if err := runner.publisher.Push(planned.dir, planned.version); err != nil {
		return fmt.Errorf("pushing %s: %w", planned.name, err)
	}
	return nil
}

// nextFreeVersion moves version past every version the registry already holds
// for chartName, one patch at a time.
//
// The derived version is taken when a chart was published without its tag
// reaching the repository: the tag lineage no longer describes the registry.
// Publishing it again would overwrite an immutable reference or fail, so the
// release takes the next free patch instead. The version is still written by
// this tool and nothing else; a backend that cannot list its versions keeps the
// derived one.
func (runner *releaseRunner) nextFreeVersion(chartName, version string) (string, error) {
	lister, ok := runner.publisher.(registry.VersionLister)
	if !ok {
		return version, nil
	}
	published, err := lister.PublishedVersions(chartName)
	if err != nil {
		return "", fmt.Errorf("reading published versions of %s: %w", chartName, err)
	}
	taken := make(map[string]struct{}, len(published))
	for _, publishedVersion := range published {
		taken[publishedVersion] = struct{}{}
	}
	for {
		if _, occupied := taken[version]; !occupied {
			return version, nil
		}
		next, err := semver.Next(version, semver.BumpPatch)
		if err != nil {
			return "", fmt.Errorf("advancing past published %s %s: %w", chartName, version, err)
		}
		_, _ = fmt.Fprintf(runner.progress(),
			"  %s: %s is already published — advancing to %s\n", chartName, version, next)
		version = next
	}
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
	_, _ = fmt.Fprintf(runner.progress(), "    GitHub Release: %s\n", url)
	return nil
}
