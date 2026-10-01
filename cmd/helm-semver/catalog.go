package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rhysmcneill/helm-semver/internal/catalog"
	igit "github.com/rhysmcneill/helm-semver/internal/git"
	"github.com/rhysmcneill/helm-semver/internal/registry"
)

type catalogSetOptions struct {
	catalog      string
	chart        string
	version      string
	cluster      string
	registry     string
	registryUser string
	registryPass string
	registryHTTP bool
	tagPrefix    string
	githubOwner  string
	githubRepo   string
}

func newCatalogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Maintain a channel catalog",
	}
	cmd.AddCommand(newCatalogSetCmd())
	return cmd
}

func newCatalogSetCmd() *cobra.Command {
	opts := &catalogSetOptions{}
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Record a published chart version in a channel catalog",
		Long: `Set records --version of --chart in --catalog with the manifest digest the
registry holds for it; a version the registry does not hold is refused. The
release commit is recorded when the release tag exists in this repository.
With --cluster the version is recorded for that cluster only
(releases.<chart>.clusters.<cluster>), refining the channel entry, which must
exist. Used to adopt versions already running, never to allocate one.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			return catalogSet(cmd, opts, gitClient, resolver)
		},
	}
	cmd.Flags().StringVar(&opts.catalog, "catalog", "", "Channel catalog YAML (required)")
	cmd.Flags().StringVar(&opts.chart, "chart", "", "Chart name (required)")
	cmd.Flags().StringVar(&opts.version, "version", "", "Published chart version (required)")
	cmd.Flags().StringVar(&opts.cluster, "cluster", "", "Record the version for this cluster only")
	cmd.Flags().StringVar(&opts.registry, "registry", "", "OCI registry URL (required)")
	cmd.Flags().StringVar(&opts.registryUser, "registry-username", "", "Registry username")
	cmd.Flags().StringVar(&opts.registryPass, "registry-password", os.Getenv("REGISTRY_PASSWORD"), "Registry password (env: REGISTRY_PASSWORD)")
	cmd.Flags().BoolVar(&opts.registryHTTP, "registry-plain-http", false, "Talk to the registry over HTTP instead of HTTPS")
	cmd.Flags().StringVar(&opts.tagPrefix, "tag-prefix", "", "Prefix for git tags, e.g. 'charts/'")
	cmd.Flags().StringVar(&opts.githubOwner, "github-owner", os.Getenv("GITHUB_REPOSITORY_OWNER"), "GitHub repository owner")
	cmd.Flags().StringVar(&opts.githubRepo, "github-repo", repositoryName(), "GitHub repository name (default: the name in GITHUB_REPOSITORY)")
	for _, name := range []string{"catalog", "chart", "version", "registry"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func catalogSet(cmd *cobra.Command, opts *catalogSetOptions, gitClient *igit.Client, resolver registry.DigestResolver) error {
	digest, err := resolver.ManifestDigest(opts.chart, opts.version)
	if err != nil {
		return fmt.Errorf("recording %s %s: %w", opts.chart, opts.version, err)
	}
	if opts.cluster != "" {
		if err := catalog.RecordCluster(opts.catalog, opts.chart, opts.cluster, catalog.Entry{Version: opts.version, Digest: digest}); err != nil {
			return fmt.Errorf("recording %s %s for %s: %w", opts.chart, opts.version, opts.cluster, err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "recorded %s %s for %s (%s)\n", opts.chart, opts.version, opts.cluster, digest)
		return nil
	}
	if opts.githubOwner == "" || opts.githubRepo == "" {
		return fmt.Errorf("the catalog receipt names the source repository: set --github-owner and --github-repo")
	}
	entry := catalog.Entry{Version: opts.version, Digest: digest, Repo: opts.githubOwner + "/" + opts.githubRepo}
	if commit, err := gitClient.TagCommit(opts.tagPrefix + opts.chart + "-v" + opts.version); err == nil {
		entry.Commit = commit
	}
	if err := catalog.Record(opts.catalog, opts.chart, entry); err != nil {
		return fmt.Errorf("recording %s %s: %w", opts.chart, opts.version, err)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "recorded %s %s (%s)\n", opts.chart, opts.version, digest)
	return nil
}
