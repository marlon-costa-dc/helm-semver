package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	helmchart "helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/helmpath"
	helmregistry "helm.sh/helm/v3/pkg/registry"
	orasregistry "oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
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
	// MaxFileBytes caps one file inside a chart at load time. Helm's loader
	// defaults to 5 MiB per file; composed umbrellas legitimately vendor
	// dependency packages above that (and a consumer vendors the umbrella
	// again), so the caller owns the limit. Zero keeps Helm's default.
	MaxFileBytes int64
	// pushed holds the manifest digest the registry confirmed per name:version.
	pushed map[string]string
}

// Push packages the chart at chartDir and pushes it to the OCI registry.
func (p *OCIPublisher) Push(chartDir, version string) error {
	// Helm's loader refuses any single file inside a chart above its own
	// 5 MiB default; the caller-owned limit takes precedence when set.
	if p.MaxFileBytes > 0 {
		helmchart.MaxDecompressedFileSize = p.MaxFileBytes
	}
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
	result, err := client.Push(data, ref)
	if err != nil {
		return fmt.Errorf("pushing %s to %s: %w", ch.Name(), p.RegistryURL, err)
	}
	if result == nil || result.Manifest == nil || result.Manifest.Digest == "" {
		return fmt.Errorf("pushing %s to %s: the registry answered no manifest digest", ch.Name(), p.RegistryURL)
	}
	if p.pushed == nil {
		p.pushed = map[string]string{}
	}
	p.pushed[ch.Name()+":"+version] = result.Manifest.Digest

	return nil
}

// PushedDigest returns the manifest digest the registry confirmed for the
// chartName version this publisher pushed.
func (p *OCIPublisher) PushedDigest(chartName, version string) (string, error) {
	digest, ok := p.pushed[chartName+":"+version]
	if !ok {
		return "", fmt.Errorf("%s %s was not pushed by this release", chartName, version)
	}
	return digest, nil
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

// CheckPush asks the registry to open an upload for chartName with the
// configured credential, the first request a push makes. 202 means the push
// would be accepted; any other answer is returned as the refusal, naming the
// chart, before anything has been published.
//
// The opened session is not cancelled: GHCR answers 405 to cancelling an
// upload (measured 2026-09-24), and a session that receives no blob writes
// nothing and expires.
func (p *OCIPublisher) CheckPush(chartName string) error {
	authorizer, err := p.authorizer()
	if err != nil {
		return err
	}
	ref, err := orasregistry.ParseReference(p.repository(chartName))
	if err != nil {
		return fmt.Errorf("parsing repository of %s: %w", chartName, err)
	}
	ctx := auth.AppendRepositoryScope(context.Background(), ref, auth.ActionPull, auth.ActionPush)
	scheme := "https"
	if p.PlainHTTP {
		scheme = "http"
	}
	url := fmt.Sprintf("%s://%s/v2/%s/blobs/uploads/", scheme, ref.Host(), ref.Repository)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("building the upload check for %s: %w", chartName, err)
	}
	response, err := authorizer.Do(request)
	if err != nil {
		return fmt.Errorf("asking %s whether %s may be published: %w", p.RegistryURL, chartName, err)
	}
	defer response.Body.Close() //nolint:errcheck
	if response.StatusCode == http.StatusAccepted {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf("%s refuses to publish %s: %s: %s",
		p.RegistryURL, chartName, response.Status, strings.TrimSpace(string(body)))
}

// authorizer is the one authorizer every request of this publisher uses, so
// the push check presents exactly the credential the push will. It resolves
// credentials as helm.sh/helm/v3/pkg/registry.NewClient does: the explicit
// username and password, else Helm's registry login store, then Docker's.
func (p *OCIPublisher) authorizer() (*auth.Client, error) {
	authorizer := &auth.Client{Client: &http.Client{Transport: helmregistry.NewTransport(false)}}
	if p.Username != "" && p.Password != "" {
		credential := auth.Credential{Username: p.Username, Password: p.Password}
		authorizer.Credential = func(_ context.Context, _ string) (auth.Credential, error) {
			return credential, nil
		}
		return authorizer, nil
	}
	options := credentials.StoreOptions{AllowPlaintextPut: true, DetectDefaultNativeStore: true}
	store, err := credentials.NewStore(helmpath.ConfigPath(helmregistry.CredentialsFileBasename), options)
	if err != nil {
		return nil, fmt.Errorf("reading the Helm registry credentials: %w", err)
	}
	if docker, err := credentials.NewStoreFromDocker(options); err == nil {
		authorizer.Credential = credentials.Credential(credentials.NewStoreWithFallbacks(store, docker))
	} else {
		authorizer.Credential = credentials.Credential(store)
	}
	return authorizer, nil
}

// client builds a registry client that authenticates through authorizer.
func (p *OCIPublisher) client() (*helmregistry.Client, error) {
	authorizer, err := p.authorizer()
	if err != nil {
		return nil, err
	}
	clientOpts := []helmregistry.ClientOption{helmregistry.ClientOptAuthorizer(*authorizer)}
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
