package chart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pinnedChart = `# consumer
apiVersion: v2
name: app
version: 0.1.0
dependencies:
  - name: lib
    version: "0.1.0"
    repository: oci://registry.example/charts
  - name: redis
    version: 18.0.0
    repository: https://charts.example
`

func chartFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Chart.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing Chart.yaml: %v", err)
	}
	return path
}

func TestDependencies_ListsTheDeclaredDependenciesInOrder(t *testing.T) {
	deps, err := Dependencies(chartFile(t, pinnedChart))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(deps) != 2 || deps[0].Name != "lib" || deps[0].Repository != "oci://registry.example/charts" || deps[1].Version != "18.0.0" {
		t.Errorf("dependencies = %+v", deps)
	}
	if _, err := Dependencies(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("a missing Chart.yaml was accepted")
	}
	if _, err := Dependencies(chartFile(t, "dependencies: [\n")); err == nil {
		t.Error("an unparsable Chart.yaml was accepted")
	}
}

func TestSetDependencyVersions_RewritesOnlyTheNamedPins(t *testing.T) {
	path := chartFile(t, pinnedChart)

	changed, err := SetDependencyVersions(path, map[string]string{"lib": "0.3.0"})
	if err != nil || !changed {
		t.Fatalf("pinning: changed=%v err=%v", changed, err)
	}

	deps, err := Dependencies(path)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if deps[0].Version != "0.3.0" || deps[1].Version != "18.0.0" {
		t.Errorf("pins = %+v, want only lib moved", deps)
	}
	data, _ := os.ReadFile(filepath.Clean(path))
	if !strings.Contains(string(data), "# consumer") || !strings.Contains(string(data), "version: 0.1.0") {
		t.Errorf("comment or chart version not preserved:\n%s", data)
	}
	if changed, err := SetDependencyVersions(path, map[string]string{"lib": "0.3.0"}); err != nil || changed {
		t.Errorf("a pin already in place reported changed=%v err=%v", changed, err)
	}
}

func TestSetDependencyVersions_RefusesWhatTheChartDoesNotDeclare(t *testing.T) {
	cases := map[string]struct {
		content string
		pins    map[string]string
	}{
		"undeclared dependency":  {pinnedChart, map[string]string{"ghost": "1.0.0"}},
		"no dependencies":        {"apiVersion: v2\nname: app\nversion: 0.1.0\n", map[string]string{"lib": "1.0.0"}},
		"dependency without pin": {"apiVersion: v2\nname: app\nversion: 0.1.0\ndependencies:\n  - name: lib\n", map[string]string{"lib": "1.0.0"}},
		"not a mapping":          {"- a\n", map[string]string{"lib": "1.0.0"}},
		"unparsable":             {"dependencies: [\n", map[string]string{"lib": "1.0.0"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := SetDependencyVersions(chartFile(t, tc.content), tc.pins); err == nil {
				t.Error("accepted")
			}
		})
	}
	if _, err := SetDependencyVersions(filepath.Join(t.TempDir(), "absent.yaml"), map[string]string{"lib": "1"}); err == nil {
		t.Error("a missing Chart.yaml was accepted")
	}
}
