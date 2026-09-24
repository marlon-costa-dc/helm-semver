package chart

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
//
// The build writes into the source chart, so it returns the function that
// removes what it wrote: the entries it added under charts/, a charts/
// directory it created and a Chart.lock it created. Whatever was there before
// the build stays. A failed build removes its own output before returning.
func BuildDependencies(chartDir string, out io.Writer) (func() error, error) {
	has, err := HasDependencies(chartDir)
	if err != nil {
		return nil, err
	}
	if !has {
		return func() error { return nil }, nil
	}

	if out == nil {
		out = io.Discard
	}

	removeBuilt, err := snapshotBuildOutput(chartDir)
	if err != nil {
		return nil, err
	}

	settings := cli.New()
	regClient, err := helmregistry.NewClient(
		helmregistry.ClientOptDebug(settings.Debug),
		helmregistry.ClientOptCredentialsFile(settings.RegistryConfig),
	)
	if err != nil {
		return nil, fmt.Errorf("creating registry client: %w", err)
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
		return nil, errors.Join(fmt.Errorf("building dependencies for chart at %s: %w", chartDir, err), removeBuilt())
	}
	return removeBuilt, nil
}

// snapshotBuildOutput records what a dependency build may write into
// chartDir — the charts/ entries and Chart.lock — and returns the function
// that removes only what appeared after the snapshot.
func snapshotBuildOutput(chartDir string) (func() error, error) {
	chartsDir := filepath.Join(chartDir, "charts")
	lockPath := filepath.Join(chartDir, "Chart.lock")

	existing := map[string]struct{}{}
	entries, err := os.ReadDir(chartsDir)
	chartsExisted := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading %s: %w", chartsDir, err)
	}
	for _, entry := range entries {
		existing[entry.Name()] = struct{}{}
	}
	_, err = os.Stat(lockPath)
	lockExisted := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading %s: %w", lockPath, err)
	}

	return func() error {
		var removed []error
		if !lockExisted {
			if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
				removed = append(removed, fmt.Errorf("removing %s: %w", lockPath, err))
			}
		}
		if !chartsExisted {
			if err := os.RemoveAll(chartsDir); err != nil {
				removed = append(removed, fmt.Errorf("removing %s: %w", chartsDir, err))
			}
			return errors.Join(removed...)
		}
		after, err := os.ReadDir(chartsDir)
		if err != nil {
			return errors.Join(append(removed, fmt.Errorf("reading %s: %w", chartsDir, err))...)
		}
		for _, entry := range after {
			if _, kept := existing[entry.Name()]; kept {
				continue
			}
			path := filepath.Join(chartsDir, entry.Name())
			if err := os.RemoveAll(path); err != nil {
				removed = append(removed, fmt.Errorf("removing %s: %w", path, err))
			}
		}
		return errors.Join(removed...)
	}, nil
}
