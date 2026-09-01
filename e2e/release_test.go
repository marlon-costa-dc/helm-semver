// Package e2e runs smoke tests against the compiled helm-semver binary using
// a temporary git repository. No external services are required.
package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// binaryPath returns the path to the compiled binary.
func binaryPath() string {
	bin := "helm-semver"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	// Makefile puts the binary at bin/helm-semver from repo root.
	root, _ := repoRoot()
	return filepath.Join(root, "bin", bin)
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

func run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(binaryPath(), args...) //nolint:gosec // binary path is resolved internally
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestBinary_Version(t *testing.T) {
	if _, err := os.Stat(binaryPath()); os.IsNotExist(err) {
		t.Skip("binary not built — run 'make build' first")
	}

	out, _, err := run(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "helm-semver") {
		t.Errorf("version output = %q, want to contain 'helm-semver'", out)
	}
}

func TestBinary_DryRun(t *testing.T) {
	if _, err := os.Stat(binaryPath()); os.IsNotExist(err) {
		t.Skip("binary not built — run 'make build' first")
	}

	// Set up a temp git repo with a chart.
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}
	wt, _ := repo.Worktree()

	// Create a minimal chart.
	chartDir := filepath.Join(dir, "charts", "myapp")
	_ = os.MkdirAll(chartDir, 0o750)
	_ = os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(`apiVersion: v2
name: myapp
version: 0.1.0
`), 0o600)

	_, _ = wt.Add("charts/myapp/Chart.yaml")
	_, err = wt.Commit("feat: initial chart", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("initial commit: %v", err)
	}

	// Run release in dry-run mode.
	cmd := exec.Command(binaryPath(), //nolint:gosec // args are test-internal constants
		"release",
		"--registry", "oci://ghcr.io/test-org/helm-charts",
		"--registry-type", "oci",
		"--charts-dir", "charts",
		"--dry-run",
		"--changelog=false",
	)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()

	out := stdout.String() + stderr.String()
	if err != nil {
		t.Logf("stdout: %s", stdout.String())
		t.Logf("stderr: %s", stderr.String())
		t.Fatalf("dry-run failed: %v", err)
	}

	if !strings.Contains(out, "myapp") {
		t.Errorf("expected 'myapp' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected '[dry-run]' marker in output, got:\n%s", out)
	}
}

// commitFile writes a file and commits it, returning nothing. Test-internal.
func commitFile(t *testing.T, repo *gogit.Repository, dir, rel, body, msg string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add(rel); err != nil {
		t.Fatalf("add %s: %v", rel, err)
	}
	if _, err := wt.Commit(msg, &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com"},
	}); err != nil {
		t.Fatalf("commit %q: %v", msg, err)
	}
}

func tagHead(t *testing.T, repo *gogit.Repository, name string) {
	t.Helper()
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if _, err := repo.CreateTag(name, head.Hash(), nil); err != nil {
		t.Fatalf("tag %q: %v", name, err)
	}
}

func dryRun(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command(binaryPath(), //nolint:gosec // args are test-internal constants
		"release",
		"--registry", "oci://ghcr.io/test-org/helm-charts",
		"--registry-type", "oci",
		"--charts-dir", "charts",
		"--dry-run",
		"--changelog=false",
		"--dependency-build=false",
	)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Logf("stdout: %s", stdout.String())
		t.Logf("stderr: %s", stderr.String())
		t.Fatalf("dry-run failed: %v", err)
	}
	return stdout.String() + stderr.String()
}

func chartYAML(name, version string) string {
	return "apiVersion: v2\nname: " + name + "\nversion: " + version + "\n"
}

// consumerYAML declares a chart that depends on another local chart, the shape
// used across cosmos-charts (file://../<owner> siblings).
func consumerYAML(name, version, dep, depVersion string) string {
	return "apiVersion: v2\nname: " + name + "\nversion: " + version +
		"\ndependencies:\n  - name: " + dep +
		"\n    version: \"" + depVersion + "\"\n    repository: \"file://../" + dep + "\"\n"
}

// TestRelease_DependencyOnlyChange_DoesNotRepublishConsumer is the operator
// contract: when only a dependency changes, the dependent chart must NOT be
// republished or bumped, and must keep its OLD dependency pin.
func TestRelease_DependencyOnlyChange_DoesNotRepublishConsumer(t *testing.T) {
	if _, err := os.Stat(binaryPath()); os.IsNotExist(err) {
		t.Skip("binary not built — run 'make build' first")
	}

	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}

	// Both charts exist and carry real released feature history, then are tagged
	// at their baseline. The consumer's own feat: commit is ALREADY released: it
	// must never be counted again, which is exactly what the cascade did.
	commitFile(t, repo, dir, "charts/lib/Chart.yaml", chartYAML("lib", "0.1.0"),
		"feat: seed lib")
	commitFile(t, repo, dir, "charts/consumer/Chart.yaml",
		consumerYAML("consumer", "0.1.0", "lib", "0.1.0"), "feat: seed consumer")
	// The baseline tags land on a CI-only commit touching neither chart — the
	// cosmos-charts bootstrap shape.
	commitFile(t, repo, dir, ".github/workflows/gates.yml", "name: gates\n",
		"fix(ci): adopt helm-semver action")
	tagHead(t, repo, "lib-v0.1.0")
	tagHead(t, repo, "consumer-v0.1.0")

	// ONLY the dependency changes.
	commitFile(t, repo, dir, "charts/lib/values.yaml", "replicas: 3\n",
		"feat: lib gains replica tuning")

	out := dryRun(t, dir)

	if !strings.Contains(out, "lib: 0.1.0 → 0.2.0") {
		t.Errorf("changed dependency 'lib' must be released; output:\n%s", out)
	}
	if strings.Contains(out, "consumer: 0.1.0 →") {
		t.Errorf("consumer was republished because its DEPENDENCY changed — the "+
			"cascade the operator forbids; output:\n%s", out)
	}
	// The tree gate answers first: the consumer is byte-identical to what its
	// tag released, so it is skipped without any commit analysis at all.
	if !strings.Contains(out, "consumer: unchanged since consumer-v0.1.0") {
		t.Errorf("consumer must be explicitly skipped; output:\n%s", out)
	}

	// The consumer keeps its OLD dependency pin on disk.
	got, err := os.ReadFile(filepath.Join(dir, "charts", "consumer", "Chart.yaml")) //nolint:gosec // test-internal path
	if err != nil {
		t.Fatalf("read consumer Chart.yaml: %v", err)
	}
	if !strings.Contains(string(got), `version: "0.1.0"`) {
		t.Errorf("consumer must keep its OLD dependency pin 0.1.0; got:\n%s", got)
	}
}

// TestRelease_UnchangedChartIsSkipped pins that a chart with no commits in its
// own directory since its tag is skipped, even when the tag was created by a
// commit that never touched that chart (the cosmos-charts bootstrap shape).
func TestRelease_UnchangedChartIsSkipped(t *testing.T) {
	if _, err := os.Stat(binaryPath()); os.IsNotExist(err) {
		t.Skip("binary not built — run 'make build' first")
	}

	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}

	commitFile(t, repo, dir, "charts/myapp/Chart.yaml", chartYAML("myapp", "0.4.120"),
		"feat: seed myapp")
	// The tag lands on a CI-only commit that never touched charts/myapp.
	commitFile(t, repo, dir, ".github/workflows/gates.yml", "name: gates\n",
		"fix(ci): use helm-semver action")
	tagHead(t, repo, "myapp-v0.4.120")

	out := dryRun(t, dir)

	if !strings.Contains(out, "myapp: unchanged since myapp-v0.4.120") {
		t.Errorf("unchanged chart must be skipped, not re-released; output:\n%s", out)
	}
	if strings.Contains(out, "would push") {
		t.Errorf("nothing changed, so nothing may be published; output:\n%s", out)
	}
}

// TestRelease_ChangedChartStillPublishes is the regression guard: the skip
// logic must not suppress a chart that genuinely changed.
func TestRelease_ChangedChartStillPublishes(t *testing.T) {
	if _, err := os.Stat(binaryPath()); os.IsNotExist(err) {
		t.Skip("binary not built — run 'make build' first")
	}

	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}

	commitFile(t, repo, dir, "charts/myapp/Chart.yaml", chartYAML("myapp", "0.4.120"),
		"feat: seed myapp")
	tagHead(t, repo, "myapp-v0.4.120")
	commitFile(t, repo, dir, "charts/myapp/values.yaml", "enabled: true\n",
		"feat: myapp gains a real feature")

	out := dryRun(t, dir)

	if !strings.Contains(out, "myapp: 0.4.120 → 0.5.0") {
		t.Errorf("a genuinely changed chart must still bump minor; output:\n%s", out)
	}
}
