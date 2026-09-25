// Package registry provides backends for publishing packaged Helm charts.
package registry

// Publisher is the interface implemented by all registry backends.
type Publisher interface {
	// Push packages the chart at chartDir and publishes it to the registry.
	Push(chartDir, version string) error
}

// PushChecker is implemented by backends that can tell, before anything is
// published, whether the configured credential may publish a chart.
//
// A release publishes chart after chart. A refusal discovered at the fifth
// chart leaves four published and the rest missing; asked before the first
// push, the same refusal costs nothing.
type PushChecker interface {
	// CheckPush returns an error naming chartName when the registry would
	// refuse to publish it with the configured credential.
	CheckPush(chartName string) error
}

// DigestResolver answers the manifest digest a published chart version has in
// the registry now.
type DigestResolver interface {
	ManifestDigest(chartName, version string) (string, error)
}

// DigestRecorder is implemented by backends whose push answers the digest the
// registry stored, so a release can record exactly what it published.
type DigestRecorder interface {
	// PushedDigest returns the manifest digest the registry confirmed for the
	// chartName version this publisher pushed; an error when it pushed none.
	PushedDigest(chartName, version string) (string, error)
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
