package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"helm.sh/helm/v3/pkg/repo/repotest"
)

// startOCIRegistry runs a real OCI registry in-process and returns a publisher
// authenticated against it.
func startOCIRegistry(t *testing.T) *OCIPublisher {
	t.Helper()
	server, err := repotest.NewOCIServer(t, t.TempDir())
	if err != nil {
		t.Fatalf("starting OCI registry: %v", err)
	}
	go server.ListenAndServe() //nolint:errcheck // stops with the test process
	return &OCIPublisher{
		RegistryURL: "oci://" + server.RegistryURL + "/charts",
		Username:    server.TestUsername,
		Password:    server.TestPassword,
		PlainHTTP:   true,
	}
}

func writeChart(t *testing.T, name, version string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating chart directory: %v", err)
	}
	chartYAML := "apiVersion: v2\nname: " + name + "\nversion: " + version + "\n"
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(chartYAML), 0o600); err != nil {
		t.Fatalf("writing Chart.yaml: %v", err)
	}
	return dir
}

func TestOCIPublisher_PublishedVersionsOfAnUnpublishedChartIsEmpty(t *testing.T) {
	publisher := startOCIRegistry(t)

	versions, err := publisher.PublishedVersions("never-pushed")

	if err != nil {
		t.Fatalf("listing an unpublished chart: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("versions = %v, want none", versions)
	}
}

func TestOCIPublisher_PublishedVersionsListsWhatWasPushed(t *testing.T) {
	publisher := startOCIRegistry(t)
	// Helm pushes in strict mode: the tag must equal the chart's own version,
	// which the release writes into Chart.yaml before it pushes.
	for _, version := range []string{"0.1.0", "0.2.0"} {
		if err := publisher.Push(writeChart(t, "app", version), version); err != nil {
			t.Fatalf("pushing app %s: %v", version, err)
		}
	}

	versions, err := publisher.PublishedVersions("app")

	if err != nil {
		t.Fatalf("listing app: %v", err)
	}
	if want := []string{"0.2.0", "0.1.0"}; !reflect.DeepEqual(versions, want) && !reflect.DeepEqual(versions, []string{"0.1.0", "0.2.0"}) {
		t.Errorf("versions = %v, want %v in either order", versions, want)
	}
}

func TestOCIPublisher_PublishedVersionsFailsLoudOnAnUnreachableRegistry(t *testing.T) {
	publisher := &OCIPublisher{RegistryURL: "oci://127.0.0.1:1/charts", PlainHTTP: true}

	versions, err := publisher.PublishedVersions("app")

	if err == nil {
		t.Fatalf("an unreachable registry answered %v; it must fail instead of reading as unpublished", versions)
	}
}
