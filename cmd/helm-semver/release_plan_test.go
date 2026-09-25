package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/repo/repotest"

	igit "github.com/rhysmcneill/helm-semver/internal/git"
	"github.com/rhysmcneill/helm-semver/internal/registry"
)

// chartRepo is a repository whose charts were each released at 0.1.0 and then
// received a feat commit, so every chart is due a minor release.
type chartRepo struct {
	root string
	repo *gogit.Repository
}

func newChartRepo(t *testing.T, charts ...string) chartRepo {
	t.Helper()
	root := t.TempDir()
	repo, err := gogit.PlainInit(root, false)
	if err != nil {
		t.Fatalf("initializing repository: %v", err)
	}
	cr := chartRepo{root: root, repo: repo}
	for _, name := range charts {
		cr.commit(t, filepath.Join("charts", name, "Chart.yaml"), "apiVersion: v2\nname: "+name+"\nversion: 0.1.0\n", "feat: seed "+name)
		head, err := repo.Head()
		if err != nil {
			t.Fatalf("reading head: %v", err)
		}
		if _, err := repo.CreateTag(name+"-v0.1.0", head.Hash(), nil); err != nil {
			t.Fatalf("tagging %s: %v", name, err)
		}
	}
	for _, name := range charts {
		cr.commit(t, filepath.Join("charts", name, "values.yaml"), "enabled: true\n", "feat: enable "+name)
	}
	return cr
}

func (cr chartRepo) commit(t *testing.T, path, content, message string) {
	t.Helper()
	full := filepath.Join(cr.root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	worktree, err := cr.repo.Worktree()
	if err != nil {
		t.Fatalf("opening worktree: %v", err)
	}
	if _, err := worktree.Add(filepath.ToSlash(path)); err != nil {
		t.Fatalf("staging %s: %v", path, err)
	}
	if _, err := worktree.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com"},
	}); err != nil {
		t.Fatalf("committing %s: %v", path, err)
	}
}

// bareOrigin adds a local bare repository as the "origin" remote.
func (cr chartRepo) bareOrigin(t *testing.T) *gogit.Repository {
	t.Helper()
	dir := t.TempDir()
	bare, err := gogit.PlainInit(dir, true)
	if err != nil {
		t.Fatalf("initializing bare remote: %v", err)
	}
	if _, err := cr.repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{dir}}); err != nil {
		t.Fatalf("adding origin: %v", err)
	}
	return bare
}

func (cr chartRepo) runner(t *testing.T, options *releaseOptions, publisher registry.Publisher, stdout, stderr *bytes.Buffer) *releaseRunner {
	t.Helper()
	gitClient, err := igit.Open(cr.root)
	if err != nil {
		t.Fatalf("opening git client: %v", err)
	}
	command := &cobra.Command{}
	command.SetOut(stdout)
	command.SetErr(stderr)
	options.chartsDir = "charts"
	options.authorName = "helm-semver[bot]"
	options.authorEmail = "bot@example.com"
	return &releaseRunner{command: command, options: options, gitClient: gitClient, publisher: publisher, repoRoot: cr.root}
}

// ociRegistry starts an in-process OCI registry. denied names charts whose
// uploads it refuses the way GHCR does (403 DENIED) while still serving reads.
func ociRegistry(t *testing.T, denied ...string) *registry.OCIPublisher {
	t.Helper()
	server, err := repotest.NewOCIServer(t, t.TempDir())
	if err != nil {
		t.Fatalf("starting OCI registry: %v", err)
	}
	go server.ListenAndServe() //nolint:errcheck // stops with the test process
	backend, err := url.Parse("http://" + server.RegistryURL)
	if err != nil {
		t.Fatalf("parsing registry URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(backend)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range denied {
			if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/charts/"+name+"/blobs/uploads/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"code":"DENIED","message":"permission_denied: write_package"}]}`))
				return
			}
		}
		// A logged-in client: the release job logs Helm in before it runs, so
		// every request here carries the registry credential.
		if r.Header.Get("Authorization") == "" {
			r.SetBasicAuth(server.TestUsername, server.TestPassword)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	return &registry.OCIPublisher{
		RegistryURL: "oci://" + strings.TrimPrefix(front.URL, "http://") + "/charts",
		Username:    server.TestUsername,
		Password:    server.TestPassword,
		PlainHTTP:   true,
	}
}

func TestReleaseRunner_JSONPlanListsTheReleasesOnStdout(t *testing.T) {
	// Given two charts due a release, only one of them selected.
	cr := newChartRepo(t, "api", "web")
	var stdout, stderr bytes.Buffer

	// When the dry-run plan is asked for as JSON.
	err := cr.runner(t, &releaseOptions{
		registry: "oci://registry.example/charts", dryRun: true, output: outputJSON, charts: []string{"web"},
	}, nil, &stdout, &stderr).run()
	if err != nil {
		t.Fatalf("planning: %v", err)
	}

	// Then stdout is exactly the plan of the selected chart, and progress is on stderr.
	var plan map[string][]planEntry
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("stdout is not a JSON plan: %v\n%s", err, stdout.String())
	}
	want := []planEntry{{
		Chart: "web", Path: "charts/web", LastTag: "web-v0.1.0",
		CurrentVersion: "0.1.0", Version: "0.2.0", Bump: "minor", Tag: "web-v0.2.0",
	}}
	if got := plan["releases"]; !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %+v, want %+v", got, want)
	}
	if !strings.Contains(stderr.String(), "web: 0.1.0 → 0.2.0 (minor)") {
		t.Errorf("progress not on stderr:\n%s", stderr.String())
	}
}

func TestReleaseRunner_ChartsNamingANonChartIsAnError(t *testing.T) {
	cr := newChartRepo(t, "api")
	var stdout, stderr bytes.Buffer

	err := cr.runner(t, &releaseOptions{
		registry: "oci://registry.example/charts", dryRun: true, charts: []string{"api", "missing"},
	}, nil, &stdout, &stderr).run()

	if err == nil || !strings.Contains(err.Error(), `"missing"`) {
		t.Fatalf("run error = %v, want --charts to reject %q", err, "missing")
	}
}

func TestReleaseRunner_ARegistryRefusalStopsTheReleaseBeforeAnyPush(t *testing.T) {
	// Given two charts due a release and a registry that refuses uploads of one.
	cr := newChartRepo(t, "api", "web")
	publisher := ociRegistry(t, "web")
	head, err := cr.repo.Head()
	if err != nil {
		t.Fatalf("reading head: %v", err)
	}
	var stdout, stderr bytes.Buffer

	// When the release runs for real.
	err = cr.runner(t, &releaseOptions{registry: publisher.RegistryURL}, publisher, &stdout, &stderr).run()

	// Then it fails naming the refused chart, and nothing was published or committed.
	if err == nil || !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), "DENIED") {
		t.Fatalf("run error = %v, want the refusal of web\n%s%s", err, stdout.String(), stderr.String())
	}
	for _, name := range []string{"api", "web"} {
		published, err := publisher.PublishedVersions(name)
		if err != nil {
			t.Fatalf("listing %s: %v", name, err)
		}
		if len(published) != 0 {
			t.Errorf("%s published %v before the refusal was known", name, published)
		}
	}
	after, err := cr.repo.Head()
	if err != nil {
		t.Fatalf("reading head: %v", err)
	}
	if after.Hash() != head.Hash() {
		t.Errorf("a release commit was made: head %s, want %s", after.Hash(), head.Hash())
	}
}

func TestReleaseRunner_EachReleaseReachesTheRemoteBeforeTheNextStarts(t *testing.T) {
	// Given two charts due a release, the second one unable to build its
	// dependencies, and a git remote to push to.
	cr := newChartRepo(t, "api", "web")
	cr.commit(t, filepath.Join("charts", "web", "Chart.yaml"),
		"apiVersion: v2\nname: web\nversion: 0.1.0\ndependencies:\n  - name: missing\n    version: 1.0.0\n    repository: file://../does-not-exist\n",
		"fix: depend on a chart that does not exist")
	origin := cr.bareOrigin(t)
	publisher := ociRegistry(t)
	var stdout, stderr bytes.Buffer

	// When the release runs for real, pushing to git.
	err := cr.runner(t, &releaseOptions{
		registry: publisher.RegistryURL, gitPush: true, dependencyBuild: true,
	}, publisher, &stdout, &stderr).run()

	// Then web fails, and api is already published and tagged on the remote.
	if err == nil || !strings.Contains(err.Error(), "web") {
		t.Fatalf("run error = %v, want web to fail", err)
	}
	if _, err := origin.Tag("api-v0.2.0"); err != nil {
		t.Errorf("api-v0.2.0 did not reach the remote before web failed: %v\n%s", err, stdout.String())
	}
	if _, err := origin.Tag("web-v0.2.0"); err == nil {
		t.Errorf("web-v0.2.0 reached the remote although web failed")
	}
	published, err := publisher.PublishedVersions("api")
	if err != nil {
		t.Fatalf("listing api: %v", err)
	}
	if len(published) != 1 || published[0] != "0.2.0" {
		t.Errorf("api versions = %v, want [0.2.0]", published)
	}
}
