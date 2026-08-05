package chart

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
)

// writeDepWorkspace creates a parent chart with a file:// dependency on a
// local subchart, so dependency resolution stays fully offline.
//
//	root/
//	├── parent/Chart.yaml  (depends on sub via file://../sub)
//	└── sub/Chart.yaml
func writeDepWorkspace(t *testing.T) (parentDir string) {
	t.Helper()
	root := t.TempDir()

	subDir := filepath.Join(root, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("creating subchart dir: %v", err)
	}
	subChart := `apiVersion: v2
name: sub
version: 0.1.0
`
	if err := os.WriteFile(filepath.Join(subDir, "Chart.yaml"), []byte(subChart), 0o600); err != nil {
		t.Fatalf("writing subchart: %v", err)
	}

	parentDir = filepath.Join(root, "parent")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("creating parent dir: %v", err)
	}
	parentChart := `apiVersion: v2
name: parent
version: 0.1.0
dependencies:
  - name: sub
    version: "0.1.0"
    repository: "file://../sub"
`
	if err := os.WriteFile(filepath.Join(parentDir, "Chart.yaml"), []byte(parentChart), 0o600); err != nil {
		t.Fatalf("writing parent chart: %v", err)
	}

	return parentDir
}

// generateLock runs the equivalent of `helm dependency update` offline
// (file:// dependency) to produce a committed Chart.lock, then removes the
// vendored charts/ directory so BuildDependencies has work to do.
func generateLock(t *testing.T, chartDir string) {
	t.Helper()
	settings := cli.New()
	man := &downloader.Manager{
		Out:              io.Discard,
		ChartPath:        chartDir,
		SkipUpdate:       true,
		Getters:          getter.All(settings),
		RepositoryConfig: settings.RepositoryConfig,
		RepositoryCache:  settings.RepositoryCache,
	}
	if err := man.Update(); err != nil {
		t.Fatalf("generating Chart.lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(chartDir, "Chart.lock")); err != nil {
		t.Fatalf("Chart.lock was not created: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(chartDir, "charts")); err != nil {
		t.Fatalf("removing vendored charts dir: %v", err)
	}
}

func packageChart(chartDir string) error {
	pkg := action.NewPackage()
	dest, err := os.MkdirTemp("", "helm-semver-test-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dest) //nolint:errcheck
	pkg.Destination = dest
	_, err = pkg.Run(chartDir, nil)
	return err
}

func TestBuildDependencies_VendorsSubchartsBeforePackaging(t *testing.T) {
	parentDir := writeDepWorkspace(t)
	generateLock(t, parentDir)

	// Defect evidence: packaging a chart whose declared dependencies are not
	// vendored in charts/ fails.
	if err := packageChart(parentDir); err == nil {
		t.Fatal("expected packaging to fail with missing dependencies, got nil")
	} else if !strings.Contains(err.Error(), "missing in charts") {
		t.Fatalf("unexpected packaging error: %v", err)
	}

	// The fix: BuildDependencies vendors the subchart before packaging.
	if err := BuildDependencies(parentDir, io.Discard); err != nil {
		t.Fatalf("BuildDependencies() error = %v", err)
	}

	vendored := filepath.Join(parentDir, "charts", "sub-0.1.0.tgz")
	if _, err := os.Stat(vendored); err != nil {
		t.Fatalf("expected vendored subchart at %s: %v", vendored, err)
	}

	if err := packageChart(parentDir); err != nil {
		t.Fatalf("packaging after dependency build failed: %v", err)
	}
}

func TestBuildDependencies_NoDependencies(t *testing.T) {
	dir := t.TempDir()
	content := `apiVersion: v2
name: standalone
version: 0.1.0
`
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("writing chart: %v", err)
	}

	if err := BuildDependencies(dir, io.Discard); err != nil {
		t.Fatalf("BuildDependencies() error = %v", err)
	}

	// A chart without dependencies must be left untouched.
	if _, err := os.Stat(filepath.Join(dir, "charts")); !os.IsNotExist(err) {
		t.Errorf("charts/ directory should not exist for a dependency-less chart")
	}
	if _, err := os.Stat(filepath.Join(dir, "Chart.lock")); !os.IsNotExist(err) {
		t.Errorf("Chart.lock should not be created for a dependency-less chart")
	}
}

func TestBuildDependencies_LockDriftFails(t *testing.T) {
	parentDir := writeDepWorkspace(t)
	generateLock(t, parentDir)

	// Drift the dependency constraint away from the committed lock.
	drifted := `apiVersion: v2
name: parent
version: 0.1.0
dependencies:
  - name: sub
    version: "0.2.0"
    repository: "file://../sub"
`
	if err := os.WriteFile(filepath.Join(parentDir, "Chart.yaml"), []byte(drifted), 0o600); err != nil {
		t.Fatalf("writing drifted chart: %v", err)
	}

	err := BuildDependencies(parentDir, io.Discard)
	if err == nil {
		t.Fatal("expected lock drift to fail, got nil")
	}
	if !strings.Contains(err.Error(), "out of sync") {
		t.Fatalf("expected lock drift error, got: %v", err)
	}
}

func TestHasDependencies(t *testing.T) {
	withDeps := writeDepWorkspace(t)
	has, err := HasDependencies(withDeps)
	if err != nil {
		t.Fatalf("HasDependencies() error = %v", err)
	}
	if !has {
		t.Error("HasDependencies() = false, want true")
	}

	dir := t.TempDir()
	content := `apiVersion: v2
name: standalone
version: 0.1.0
`
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("writing chart: %v", err)
	}
	has, err = HasDependencies(dir)
	if err != nil {
		t.Fatalf("HasDependencies() error = %v", err)
	}
	if has {
		t.Error("HasDependencies() = true, want false")
	}
}
