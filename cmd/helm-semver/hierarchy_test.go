package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/rhysmcneill/helm-semver/internal/chart"
)

// hierarchyRepo holds a library chart "lib" and a consumer "app" that pins lib
// on registryURL. Both were released at 0.1.0 and then changed, so both are
// due a release in the same run.
func hierarchyRepo(t *testing.T, registryURL string) chartRepo {
	t.Helper()
	cr := newChartRepo(t, "lib")
	cr.commit(t, filepath.Join("charts", "app", "Chart.yaml"),
		"apiVersion: v2\nname: app\nversion: 0.1.0\ndependencies:\n"+
			"  - name: lib\n    version: \"0.1.0\"\n    repository: \""+registryURL+"\"\n",
		"feat: seed app")
	head, err := cr.repo.Head()
	if err != nil {
		t.Fatalf("reading head: %v", err)
	}
	if _, err := cr.repo.CreateTag("app-v0.1.0", head.Hash(), nil); err != nil {
		t.Fatalf("tagging app: %v", err)
	}
	cr.commit(t, filepath.Join("charts", "app", "values.yaml"), "replicas: 2\n", "fix: scale app")
	return cr
}

// isolateHelm points Helm's config, cache and data at the test's own
// directories, so a dependency build never reads or writes the user's Helm state.
func isolateHelm(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HELM_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HELM_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("HELM_DATA_HOME", filepath.Join(home, "data"))
}

func writeCatalog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "releases.yaml")
	if err := os.WriteFile(path, []byte("# channel catalog\nglobal:\n  charts:\n    releases: {}\n"), 0o600); err != nil {
		t.Fatalf("writing catalog: %v", err)
	}
	return path
}

func dependencyVersion(t *testing.T, root, chartName, dependency string) string {
	t.Helper()
	deps, err := chart.Dependencies(filepath.Join(root, "charts", chartName, "Chart.yaml"))
	if err != nil {
		t.Fatalf("reading dependencies of %s: %v", chartName, err)
	}
	for _, dep := range deps {
		if dep.Name == dependency {
			return dep.Version
		}
	}
	t.Fatalf("%s declares no %s", chartName, dependency)
	return ""
}

func TestRelease_AChildPublishesAgainstTheParentReleasedInTheSameRun(t *testing.T) {
	// Given a parent and a child that both changed, the child pinning the old parent.
	publisher := ociRegistry(t)
	isolateHelm(t)
	cr := hierarchyRepo(t, publisher.RegistryURL)
	catalogPath := writeCatalog(t)
	var stdout, stderr bytes.Buffer

	// When the release runs with a catalog.
	err := cr.runner(t, &releaseOptions{
		registry: publisher.RegistryURL, registryType: "oci", catalog: catalogPath,
		githubOwner: "acme", githubRepo: "charts", dependencyBuild: true,
	}, publisher, &stdout, &stderr).run()
	if err != nil {
		t.Fatalf("release: %v\n%s", err, stderr.String())
	}

	// Then the parent was published first and the child pins exactly that version.
	if got := dependencyVersion(t, cr.root, "app", "lib"); got != "0.2.0" {
		t.Errorf("app pins lib %s, want the 0.2.0 this run published", got)
	}
	libAt := strings.Index(stdout.String(), "lib: 0.1.0")
	appAt := strings.Index(stdout.String(), "app: 0.1.0")
	if libAt < 0 || appAt < 0 {
		t.Fatalf("plan lines missing:\n%s", stdout.String())
	}
	// And the catalog records both with the digest the registry confirmed.
	data, err := os.ReadFile(filepath.Clean(catalogPath))
	if err != nil {
		t.Fatalf("reading catalog: %v", err)
	}
	var doc struct {
		Global struct {
			Charts struct {
				Releases map[string]map[string]string `yaml:"releases"`
			} `yaml:"charts"`
		} `yaml:"global"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing catalog: %v", err)
	}
	releases := doc.Global.Charts.Releases
	for name, version := range map[string]string{"lib": "0.2.0", "app": "0.1.1"} {
		entry := releases[name]
		if entry["version"] != version || !strings.HasPrefix(entry["digest"], "sha256:") || entry["repo"] != "acme/charts" || entry["commit"] == "" {
			t.Errorf("catalog %s = %v, want version %s with a sha256 digest and receipt", name, entry, version)
		}
	}
}

func TestRelease_AChangeNoConventionalCommitNamesIsStillAPatch(t *testing.T) {
	// Given a chart whose only change since its tag is a chore commit.
	cr := newChartRepo(t)
	cr.commit(t, filepath.Join("charts", "ops", "Chart.yaml"), "apiVersion: v2\nname: ops\nversion: 0.1.0\n", "feat: seed ops")
	head, err := cr.repo.Head()
	if err != nil {
		t.Fatalf("reading head: %v", err)
	}
	if _, err := cr.repo.CreateTag("ops-v0.1.0", head.Hash(), nil); err != nil {
		t.Fatalf("tagging: %v", err)
	}
	cr.commit(t, filepath.Join("charts", "ops", "values.yaml"), "a: 1\n", "chore: tidy ops")
	var stdout, stderr bytes.Buffer

	// When the plan is asked for.
	err = cr.runner(t, &releaseOptions{registry: "oci://registry.example/charts", dryRun: true}, nil, &stdout, &stderr).run()
	if err != nil {
		t.Fatalf("planning: %v", err)
	}

	// Then the changed tree is released as a patch.
	if !strings.Contains(stdout.String(), "ops: 0.1.0 → 0.1.1 (patch)") {
		t.Errorf("a changed tree was not planned as a patch:\n%s", stdout.String())
	}
}

func TestRelease_AnUnchangedChildIsNotReleasedWhenItsParentIs(t *testing.T) {
	// Given a parent that changed and a child whose tree matches its tag.
	publisher := ociRegistry(t)
	cr := newChartRepo(t, "lib")
	cr.commit(t, filepath.Join("charts", "app", "Chart.yaml"),
		"apiVersion: v2\nname: app\nversion: 0.1.0\ndependencies:\n"+
			"  - name: lib\n    version: \"0.1.0\"\n    repository: \""+publisher.RegistryURL+"\"\n",
		"feat: seed app")
	head, err := cr.repo.Head()
	if err != nil {
		t.Fatalf("reading head: %v", err)
	}
	if _, err := cr.repo.CreateTag("app-v0.1.0", head.Hash(), nil); err != nil {
		t.Fatalf("tagging app: %v", err)
	}
	var stdout, stderr bytes.Buffer

	// When the release runs.
	err = cr.runner(t, &releaseOptions{registry: publisher.RegistryURL, registryType: "oci"}, publisher, &stdout, &stderr).run()
	if err != nil {
		t.Fatalf("release: %v\n%s", err, stderr.String())
	}

	// Then only the parent is released and the child keeps its pin.
	if strings.Contains(stdout.String(), "app: 0.1.0 →") {
		t.Errorf("an unchanged child was released:\n%s", stdout.String())
	}
	if got := dependencyVersion(t, cr.root, "app", "lib"); got != "0.1.0" {
		t.Errorf("unchanged app pin moved to %s", got)
	}
}

func TestRelease_AFailedValidationLeavesNoPinAndNoRelease(t *testing.T) {
	// Given a parent and child due together, and a gate that refuses the child.
	publisher := ociRegistry(t)
	cr := hierarchyRepo(t, publisher.RegistryURL)
	var stdout, stderr bytes.Buffer

	// When the release runs.
	err := cr.runner(t, &releaseOptions{
		registry: publisher.RegistryURL, registryType: "oci",
		validateCmd: `test "$HELM_SEMVER_CHART" != app`,
	}, publisher, &stdout, &stderr).run()

	// Then it fails naming the child, whose Chart.yaml is exactly as before.
	if err == nil || !strings.Contains(err.Error(), "validating app") {
		t.Fatalf("release error = %v, want the app validation failure", err)
	}
	if got := dependencyVersion(t, cr.root, "app", "lib"); got != "0.1.0" {
		t.Errorf("app pin left at %s after a failed validation, want 0.1.0", got)
	}
	metadata, err := chart.Load(filepath.Join(cr.root, "charts", "app", "Chart.yaml"))
	if err != nil {
		t.Fatalf("loading app: %v", err)
	}
	if metadata.Version != "0.1.0" {
		t.Errorf("app version bumped to %s by a refused release", metadata.Version)
	}
	if _, err := cr.repo.Tag("app-v0.1.1"); err == nil {
		t.Error("a refused chart was tagged")
	}
}
