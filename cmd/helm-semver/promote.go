package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rhysmcneill/helm-semver/internal/catalog"
	igit "github.com/rhysmcneill/helm-semver/internal/git"
	"github.com/rhysmcneill/helm-semver/internal/registry"
)

type promoteOptions struct {
	chartsDir    string
	from         string
	to           string
	registry     string
	registryUser string
	registryPass string
	registryHTTP bool
	tagPrefix    string
	githubOwner  string
	githubRepo   string
}

func newPromoteCmd() *cobra.Command {
	opts := &promoteOptions{}
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Record in a channel catalog the chart versions released on this branch",
		Long: `Promote writes, for every chart with a release tag reachable from HEAD, the
newest such version into the --to catalog, with the manifest digest the
registry holds for it and the tag's commit. When the --from catalog (the
channel the branch was promoted from) records the same version, its digest
must equal the registry's: the promoted artifact is the one validated there.
Nothing is rebuilt or pushed.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPromote(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.chartsDir, "charts-dir", "charts", "Root directory containing chart subdirectories")
	cmd.Flags().StringVar(&opts.from, "from", "", "Catalog of the channel the branch was promoted from (required)")
	cmd.Flags().StringVar(&opts.to, "to", "", "Catalog of the channel to record the promoted versions in (required)")
	cmd.Flags().StringVar(&opts.registry, "registry", "", "OCI registry URL (required)")
	cmd.Flags().StringVar(&opts.registryUser, "registry-username", "", "Registry username")
	cmd.Flags().StringVar(&opts.registryPass, "registry-password", os.Getenv("REGISTRY_PASSWORD"), "Registry password (env: REGISTRY_PASSWORD)")
	cmd.Flags().BoolVar(&opts.registryHTTP, "registry-plain-http", false, "Talk to the registry over HTTP instead of HTTPS")
	cmd.Flags().StringVar(&opts.tagPrefix, "tag-prefix", "", "Prefix for git tags, e.g. 'charts/'")
	cmd.Flags().StringVar(&opts.githubOwner, "github-owner", os.Getenv("GITHUB_REPOSITORY_OWNER"), "GitHub repository owner")
	cmd.Flags().StringVar(&opts.githubRepo, "github-repo", repositoryName(), "GitHub repository name (default: the name in GITHUB_REPOSITORY)")
	for _, name := range []string{"from", "to", "registry"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func runPromote(cmd *cobra.Command, opts *promoteOptions) error {
	root, err := findRepoRoot()
	if err != nil {
		return fmt.Errorf("finding repository root: %w", err)
	}
	gitClient, err := igit.Open(root)
	if err != nil {
		return fmt.Errorf("opening git repository: %w", err)
	}
	resolver := &registry.OCIPublisher{
		RegistryURL: opts.registry, Username: opts.registryUser,
		Password: opts.registryPass, PlainHTTP: opts.registryHTTP,
	}
	return promote(cmd, opts, root, gitClient, resolver)
}

func promote(cmd *cobra.Command, opts *promoteOptions, root string, gitClient *igit.Client, resolver registry.DigestResolver) error {
	if opts.githubOwner == "" || opts.githubRepo == "" {
		return fmt.Errorf("the catalog receipt names the source repository: set --github-owner and --github-repo")
	}
	source, err := catalog.Entries(opts.from)
	if err != nil {
		return fmt.Errorf("reading the source catalog: %w", err)
	}
	current, err := catalog.Entries(opts.to)
	if err != nil {
		return fmt.Errorf("reading the target catalog: %w", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, opts.chartsDir))
	if err != nil {
		return fmt.Errorf("reading %s: %w", opts.chartsDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, err := os.Stat(filepath.Join(root, opts.chartsDir, entry.Name(), "Chart.yaml")); entry.IsDir() && err == nil {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	out := cmd.OutOrStdout()
	for _, name := range names {
		tag, err := gitClient.LatestTag(opts.tagPrefix + name)
		if err != nil {
			return fmt.Errorf("reading the release tag of %s: %w", name, err)
		}
		if tag == "" {
			continue
		}
		version := strings.TrimPrefix(tag, opts.tagPrefix+name+"-v")
		digest, err := resolver.ManifestDigest(name, version)
		if err != nil {
			return fmt.Errorf("promoting %s %s: %w", name, version, err)
		}
		if validated, known := source[name]; known && validated.Version == version && validated.Digest != digest {
			return fmt.Errorf("%s %s: the registry holds %s but %s validated %s", name, version, digest, opts.from, validated.Digest)
		}
		commit, err := gitClient.TagCommit(tag)
		if err != nil {
			return fmt.Errorf("promoting %s %s: %w", name, version, err)
		}
		entry := catalog.Entry{Version: version, Digest: digest, Repo: opts.githubOwner + "/" + opts.githubRepo, Commit: commit}
		if current[name] == entry {
			continue
		}
		if err := catalog.Record(opts.to, name, entry); err != nil {
			return fmt.Errorf("promoting %s %s: %w", name, version, err)
		}
		_, _ = fmt.Fprintf(out, "promoted %s %s (%s)\n", name, version, digest)
	}
	return nil
}
