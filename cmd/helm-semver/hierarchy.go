package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mmsemver "github.com/Masterminds/semver/v3"

	"github.com/rhysmcneill/helm-semver/internal/chart"
	"github.com/rhysmcneill/helm-semver/internal/registry"
)

// internalDependencies returns the dependencies of chartDir that this release
// owns: pinned on the release registry and present as a chart in the charts
// directory. Upstream dependencies are none of its business.
func (runner *releaseRunner) internalDependencies(chartDir string) ([]string, error) {
	declared, err := chart.Dependencies(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		return nil, fmt.Errorf("reading internal dependencies: %w", err)
	}
	chartsDir := filepath.Join(runner.repoRoot, runner.options.chartsDir)
	names := make([]string, 0, len(declared))
	for _, dependency := range declared {
		if !sameRegistry(dependency.Repository, runner.options.registry) {
			continue
		}
		if _, err := os.Stat(filepath.Join(chartsDir, dependency.Name, "Chart.yaml")); err != nil {
			continue
		}
		names = append(names, dependency.Name)
	}
	return names, nil
}

func sameRegistry(a, b string) bool {
	return a != "" && strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

// orderPlan sorts the plan parents first: a chart comes after every planned
// chart it depends on, so each child is validated and published against the
// parent versions this very run publishes. Ties keep the charts-dir order. A
// dependency cycle among planned charts has no valid order and is an error.
func orderPlan(plan []plannedRelease) ([]plannedRelease, error) {
	index := make(map[string]int, len(plan))
	for i, planned := range plan {
		index[planned.name] = i
	}
	pending := make([]int, len(plan))
	children := make(map[string][]string, len(plan))
	for i, planned := range plan {
		for _, parent := range planned.internal {
			if _, inPlan := index[parent]; inPlan && parent != planned.name {
				pending[i]++
				children[parent] = append(children[parent], planned.name)
			}
		}
	}
	ready := make([]int, 0, len(plan))
	for i := range plan {
		if pending[i] == 0 {
			ready = append(ready, i)
		}
	}
	ordered := make([]plannedRelease, 0, len(plan))
	for len(ready) > 0 {
		sort.Ints(ready)
		current := ready[0]
		ready = ready[1:]
		ordered = append(ordered, plan[current])
		for _, child := range children[plan[current].name] {
			childIndex := index[child]
			pending[childIndex]--
			if pending[childIndex] == 0 {
				ready = append(ready, childIndex)
			}
		}
	}
	if len(ordered) != len(plan) {
		cyclic := make([]string, 0)
		for i, count := range pending {
			if count > 0 {
				cyclic = append(cyclic, plan[i].name)
			}
		}
		return nil, fmt.Errorf("planned charts depend on each other in a cycle: %s", strings.Join(cyclic, ", "))
	}
	return ordered, nil
}

// pinsFor decides the version planned adopts for each internal dependency: the
// version this run publishes when the parent is in the plan, otherwise the
// newest version the registry holds. A parent the registry has never published
// cannot be pinned and is an error.
func (runner *releaseRunner) pinsFor(planned plannedRelease, plannedVersions map[string]string) (map[string]string, error) {
	pins := make(map[string]string, len(planned.internal))
	for _, parent := range planned.internal {
		if version, inPlan := plannedVersions[parent]; inPlan {
			pins[parent] = version
			continue
		}
		newest, err := runner.newestPublished(parent)
		if err != nil {
			return nil, fmt.Errorf("%s depends on %s: %w", planned.name, parent, err)
		}
		pins[parent] = newest
	}
	return pins, nil
}

// newestPublished returns the highest semver the registry holds for chartName.
func (runner *releaseRunner) newestPublished(chartName string) (string, error) {
	if newest, known := runner.newest[chartName]; known {
		return newest, nil
	}
	lister, ok := runner.publisher.(registry.VersionLister)
	if !ok {
		return "", fmt.Errorf("the %s backend cannot list published versions to pin %s", runner.options.registryType, chartName)
	}
	published, err := lister.PublishedVersions(chartName)
	if err != nil {
		return "", fmt.Errorf("reading published versions of %s: %w", chartName, err)
	}
	var newest *mmsemver.Version
	for _, raw := range published {
		version, err := mmsemver.StrictNewVersion(raw)
		if err != nil {
			continue
		}
		if newest == nil || version.GreaterThan(newest) {
			newest = version
		}
	}
	if newest == nil {
		return "", fmt.Errorf("%s has no published version to pin", chartName)
	}
	runner.newest[chartName] = newest.String()
	return newest.String(), nil
}

// confirmPublished fails unless the registry now holds chartName at version:
// a pin to a parent released earlier in this run must name what was pushed.
func (runner *releaseRunner) confirmPublished(chartName, version string) error {
	lister, ok := runner.publisher.(registry.VersionLister)
	if !ok {
		return fmt.Errorf("the %s backend cannot confirm %s %s", runner.options.registryType, chartName, version)
	}
	published, err := lister.PublishedVersions(chartName)
	if err != nil {
		return fmt.Errorf("reading published versions of %s: %w", chartName, err)
	}
	for _, held := range published {
		if held == version {
			return nil
		}
	}
	return fmt.Errorf("%s %s is pinned but the registry does not hold it", chartName, version)
}
