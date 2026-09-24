package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	helmchart "helm.sh/helm/v3/pkg/chart/loader"
	helmregistry "helm.sh/helm/v3/pkg/registry"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// OCIPublisher pushes charts to an OCI-compliant registry (GHCR, ECR, ACR…).
type OCIPublisher struct {
	// RegistryURL is the OCI registry URL, e.g. "oci://ghcr.io/my-org/helm-charts".
	RegistryURL string
	// Username and Password for registry authentication.
	Username string
	Password string
	// PlainHTTP talks to the registry over HTTP instead of HTTPS.
	PlainHTTP bool
}

// Push packages the chart at chartDir and pushes it to the OCI registry.
func (p *OCIPublisher) Push(chartDir, version string) error {
	// Load chart metadata to extract the name.
	ch, err := helmchart.Load(chartDir)
	if err != nil {
		return fmt.Errorf("loading chart at %s: %w", chartDir, err)
	}

	// Create a temp dir for the packaged .tgz.
	tmpDir, err := os.MkdirTemp("", "helm-semver-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	// Package the chart.
	pkg := action.NewPackage()
	pkg.Destination = tmpDir
	tgzPath, err := pkg.Run(chartDir, nil)
	if err != nil {
		return fmt.Errorf("packaging chart %s: %w", ch.Name(), err)
	}

	client, err := p.client()
	if err != nil {
		return err
	}

	// Read the packaged chart.
	data, err := os.ReadFile(tgzPath) // #nosec
	if err != nil {
		return fmt.Errorf("reading packaged chart: %w", err)
	}

	ref := fmt.Sprintf("%s:%s", p.repository(filepath.Base(ch.Name())), version)
	if _, err = client.Push(data, ref); err != nil {
		return fmt.Errorf("pushing %s to %s: %w", ch.Name(), p.RegistryURL, err)
	}

	return nil
}

// PublishedVersions lists the versions of chartName the registry holds.
//
// Only the registry's own NAME_UNKNOWN answer means "never published" and yields
// an empty list. Every other failure — an unreachable registry, a refused
// credential — is returned: reading it as "nothing published" would let the
// release overwrite a version it could not see.
func (p *OCIPublisher) PublishedVersions(chartName string) ([]string, error) {
	client, err := p.client()
	if err != nil {
		return nil, err
	}
	tags, err := client.Tags(p.repository(chartName))
	if err != nil {
		if repositoryUnknown(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing published versions of %s in %s: %w", chartName, p.RegistryURL, err)
	}
	return tags, nil
}

// repositoryUnknown reports whether the registry answered that the repository
// does not exist — the OCI NAME_UNKNOWN code, and nothing inferred from a status.
func repositoryUnknown(err error) bool {
	var response *errcode.ErrorResponse
	if !errors.As(err, &response) {
		return false
	}
	for _, registryErr := range response.Errors {
		if registryErr.Code == errcode.ErrorCodeNameUnknown {
			return true
		}
	}
	return false
}

// client builds a registry client carrying the configured credentials.
func (p *OCIPublisher) client() (*helmregistry.Client, error) {
	var clientOpts []helmregistry.ClientOption
	if p.Username != "" && p.Password != "" {
		clientOpts = append(clientOpts,
			helmregistry.ClientOptBasicAuth(p.Username, p.Password),
		)
	}
	if p.PlainHTTP {
		clientOpts = append(clientOpts, helmregistry.ClientOptPlainHTTP())
	}
	client, err := helmregistry.NewClient(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("creating registry client: %w", err)
	}
	return client, nil
}

// repository returns the registry repository of one chart, without a tag.
func (p *OCIPublisher) repository(chartName string) string {
	// Strip the "oci://" scheme prefix that helm registry login requires.
	registryBase := strings.TrimPrefix(p.RegistryURL, helmregistry.OCIScheme+"://")
	return fmt.Sprintf("%s/%s", registryBase, chartName)
}
