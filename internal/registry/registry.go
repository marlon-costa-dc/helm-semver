// Package registry provides backends for publishing packaged Helm charts.
package registry

// Publisher is the interface implemented by all registry backends.
type Publisher interface {
	// Push packages the chart at chartDir and publishes it to the registry.
	Push(chartDir, version string) error
}

// VersionLister is implemented by backends that can answer which versions of a
// chart are already published.
//
// The version a release derives from the commits can already be taken: a chart
// may have been published without its tag ever reaching the repository, so the
// tag lineage the release reads no longer describes the registry. Publishing the
// derived version again would overwrite an immutable reference or fail. A
// backend that can list what it holds lets the release move past those versions
// instead.
type VersionLister interface {
	// PublishedVersions returns every version of chartName the registry holds.
	// A chart the registry has never seen answers an empty list; any other
	// failure to read the registry is an error, never an empty answer.
	PublishedVersions(chartName string) ([]string, error)
}
