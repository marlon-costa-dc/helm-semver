package chart

import (
	"fmt"
	"io"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	helmregistry "helm.sh/helm/v3/pkg/registry"
)

// HasDependencies reports whether the chart at chartDir declares any
// dependencies in its Chart.yaml.
func HasDependencies(chartDir string) (bool, error) {
	ch, err := loader.Load(chartDir)
	if err != nil {
		return false, fmt.Errorf("loading chart at %s: %w", chartDir, err)
	}
	return len(ch.Metadata.Dependencies) > 0, nil
}

// BuildDependencies vendors the dependencies declared in Chart.yaml into the
// chart's charts/ directory before packaging, equivalent to running
// `helm dependency build`.
//
// Like the Helm CLI, it respects a committed Chart.lock: dependencies are
// fetched exactly as locked and a lock that is out of sync with Chart.yaml
// fails loudly instead of silently drifting. Charts that declare no
// dependencies are left untouched.
func BuildDependencies(chartDir string, out io.Writer) error {
	has, err := HasDependencies(chartDir)
	if err != nil {
		return err
	}
	if !has {
		return nil
	}

	if out == nil {
		out = io.Discard
	}

	settings := cli.New()
	regClient, err := helmregistry.NewClient(
		helmregistry.ClientOptDebug(settings.Debug),
		helmregistry.ClientOptCredentialsFile(settings.RegistryConfig),
	)
	if err != nil {
		return fmt.Errorf("creating registry client: %w", err)
	}

	man := &downloader.Manager{
		Out:              out,
		ChartPath:        chartDir,
		SkipUpdate:       true,
		Getters:          getter.All(settings),
		RegistryClient:   regClient,
		RepositoryConfig: settings.RepositoryConfig,
		RepositoryCache:  settings.RepositoryCache,
		Debug:            settings.Debug,
	}
	if err := man.Build(); err != nil {
		return fmt.Errorf("building dependencies for chart at %s: %w", chartDir, err)
	}
	return nil
}
