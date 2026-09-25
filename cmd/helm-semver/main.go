// Package main is the entry point for the helm-semver CLI.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	igit "github.com/rhysmcneill/helm-semver/internal/git"
	"github.com/rhysmcneill/helm-semver/internal/registry"
	"github.com/rhysmcneill/helm-semver/internal/version"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "helm-semver",
		Short: "Semver release automation for Helm chart monorepos",
		Long: `helm-semver bumps Helm chart versions from conventional commits,
packages and pushes to OCI, ChartMuseum, or GitHub Pages registries,
and optionally generates changelogs and GitHub Releases.`,
	}

	root.AddCommand(newReleaseCmd())
	root.AddCommand(newVersionCmd())

	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print helm-semver version information",
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "helm-semver %s (commit: %s, built: %s)\n",
				version.Version, version.Commit, version.BuildDate)
		},
	}
}

// outputJSON selects the machine-readable release plan on stdout.
const outputJSON = "json"

type releaseOptions struct {
	chartsDir       string
	charts          []string
	output          string
	registry        string
	registryType    string
	registryUser    string
	registryPass    string
	registryHTTP    bool
	gitPush         bool
	dryRun          bool
	changelog       bool
	githubRelease   bool
	dependencyBuild bool
	gitToken        string
	githubOwner     string
	githubRepo      string
	tagPrefix       string
	authorName      string
	authorEmail     string
	maxFileBytes    int64
	validateCmd     string
	catalog         string
}

func newReleaseCmd() *cobra.Command {
	opts := &releaseOptions{}

	cmd := &cobra.Command{
		Use:   "release",
		Short: "Release changed charts based on conventional commits",
		Long: `Scans each chart directory for conventional commits since the last release tag,
bumps the chart version (fix→patch, feat→minor, feat!→major), packages the chart,
and pushes it to the configured registry.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRelease(cmd, opts)
		},
	}

	cmd.Flags().StringVar(&opts.chartsDir, "charts-dir", "charts", "Root directory containing chart subdirectories")
	cmd.Flags().StringSliceVar(&opts.charts, "charts", nil, "Release only these charts (names under --charts-dir); a name that is not a chart is an error")
	cmd.Flags().StringVar(&opts.output, "output", "text", "Output format: text, or json (the planned or published releases on stdout; progress on stderr)")
	cmd.Flags().StringVar(&opts.registry, "registry", "", "Registry URL (required)")
	cmd.Flags().StringVar(&opts.registryType, "registry-type", "oci", "Registry type: oci, chartmuseum, github-pages")
	cmd.Flags().StringVar(&opts.registryUser, "registry-username", "", "Registry username")
	cmd.Flags().StringVar(&opts.registryPass, "registry-password", os.Getenv("REGISTRY_PASSWORD"), "Registry password (env: REGISTRY_PASSWORD)")
	cmd.Flags().BoolVar(&opts.registryHTTP, "registry-plain-http", false, "Talk to an OCI registry over HTTP instead of HTTPS")
	cmd.Flags().BoolVar(&opts.gitPush, "git-push", true, "Push version bump commit and tags")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Print what would happen without making any changes")
	cmd.Flags().BoolVar(&opts.changelog, "changelog", true, "Append release entry to CHANGELOG.md per chart")
	cmd.Flags().BoolVar(&opts.githubRelease, "github-release", false, "Create a GitHub Release for each chart")
	cmd.Flags().BoolVar(&opts.dependencyBuild, "dependency-build", true, "Build chart dependencies (helm dependency build) before packaging")
	cmd.Flags().StringVar(&opts.gitToken, "git-token", os.Getenv("GITHUB_TOKEN"), "Token for git push authentication (env: GITHUB_TOKEN)")
	// --github-token is a deprecated alias kept for backwards compatibility.
	cmd.Flags().StringVar(&opts.gitToken, "github-token", os.Getenv("GITHUB_TOKEN"), "Deprecated: use --git-token")
	if err := cmd.Flags().MarkHidden("github-token"); err != nil {
		panic(err)
	}
	cmd.Flags().StringVar(&opts.githubOwner, "github-owner", os.Getenv("GITHUB_REPOSITORY_OWNER"), "GitHub repository owner")
	cmd.Flags().StringVar(&opts.githubRepo, "github-repo", "", "GitHub repository name")
	cmd.Flags().StringVar(&opts.tagPrefix, "tag-prefix", "", "Prefix for git tags, e.g. 'charts/'")
	cmd.Flags().StringVar(&opts.authorName, "git-author-name", "helm-semver[bot]", "Git commit author name")
	cmd.Flags().StringVar(&opts.authorEmail, "git-author-email", "helm-semver[bot]@users.noreply.github.com", "Git commit author email")
	cmd.Flags().StringVar(&opts.validateCmd, "validate-cmd", "", "Shell command run per chart after its parent pins are adopted and before it is published; HELM_SEMVER_CHART and HELM_SEMVER_CHART_DIR name the chart; a non-zero exit stops the release")
	cmd.Flags().StringVar(&opts.catalog, "catalog", "", "Channel catalog YAML to record each published chart in (global.charts.releases.<chart>: version, digest, repo, commit)")
	cmd.Flags().Int64Var(&opts.maxFileBytes, "max-file-bytes", 32*1024*1024, "Maximum size in bytes Helm's loader accepts for one file inside a chart (vendored dependency packages exceed Helm's 5 MiB default)")

	_ = cmd.MarkFlagRequired("registry")

	return cmd
}

func runRelease(cmd *cobra.Command, opts *releaseOptions) error {
	if opts.output != "text" && opts.output != outputJSON {
		return fmt.Errorf("unknown --output %q: must be text or json", opts.output)
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		return fmt.Errorf("finding repository root: %w", err)
	}

	gitClient, err := igit.Open(repoRoot)
	if err != nil {
		return fmt.Errorf("opening git repository: %w", err)
	}

	pub, err := newPublisher(opts)
	if err != nil {
		return fmt.Errorf("initialising publisher: %w", err)
	}

	return (&releaseRunner{
		command: cmd, options: opts, gitClient: gitClient, publisher: pub, repoRoot: repoRoot,
	}).run()
}

func newPublisher(opts *releaseOptions) (registry.Publisher, error) {
	switch strings.ToLower(opts.registryType) {
	case "oci":
		return &registry.OCIPublisher{
			RegistryURL:  opts.registry,
			Username:     opts.registryUser,
			Password:     opts.registryPass,
			PlainHTTP:    opts.registryHTTP,
			MaxFileBytes: opts.maxFileBytes,
		}, nil
	case "chartmuseum":
		return &registry.ChartMuseumPublisher{
			BaseURL:  opts.registry,
			Username: opts.registryUser,
			Password: opts.registryPass,
		}, nil
	case "github-pages":
		return &registry.GitHubPagesPublisher{
			RepoURL:  opts.registry,
			RepoPath: ".",
		}, nil
	default:
		return nil, fmt.Errorf("unknown registry type %q: must be one of oci, chartmuseum, github-pages", opts.registryType)
	}
}

// findRepoRoot walks up from the current directory to find the git root.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not inside a git repository")
		}
		dir = parent
	}
}
