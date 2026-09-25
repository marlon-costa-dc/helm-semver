package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	helmchart "helm.sh/helm/v3/pkg/chart/loader"
)

func TestVersionCmd(t *testing.T) {
	root := newRootCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetArgs([]string{"version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("version cmd error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "helm-semver") {
		t.Errorf("version output missing 'helm-semver': %q", out)
	}
}

func TestReleaseCmd_RequiresRegistry(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"release"})
	// Should fail because --registry is required.
	err := root.Execute()
	if err == nil {
		t.Error("expected error when --registry is missing, got nil")
	}
}

func TestReleaseCmd_UnknownRegistryType(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{
		"release",
		"--registry", "https://example.com",
		"--registry-type", "unknown",
		"--dry-run",
	})
	err := root.Execute()
	if err == nil {
		t.Error("expected error for unknown registry type, got nil")
	}
}

// Composed umbrellas vendor dependency packages that legitimately exceed
// Helm's 5 MiB per-file loader default; --max-file-bytes owns the limit.
func TestMaxFileBytesCalibratesHelmLoader(t *testing.T) {
	dir := t.TempDir()
	meta := "apiVersion: v2\nname: bigfile\nversion: 0.1.0\ntype: application\n"
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(meta), 0o644); err != nil {
		t.Fatalf("writing Chart.yaml: %v", err)
	}
	// A real gzip member of 6 MiB: the loader validates the archive and
	// measures the decompressed size of each member.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, member := range []struct {
		name string
		body []byte
	}{
		{name: "bigdep/Chart.yaml", body: []byte("apiVersion: v2\nname: bigdep\nversion: 0.1.0\n")},
		{name: "bigdep/files/big.bin", body: make([]byte, 6*1024*1024)},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: member.name,
			Mode: 0o644,
			Size: int64(len(member.body)),
		}); err != nil {
			t.Fatalf("writing tar header: %v", err)
		}
		if _, err := tw.Write(member.body); err != nil {
			t.Fatalf("writing tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "charts"), 0o755); err != nil {
		t.Fatalf("creating charts dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "charts", "bigdep-0.1.0.tgz"), buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing vendored package: %v", err)
	}

	defer func(prev int64) { helmchart.MaxDecompressedFileSize = prev }(
		helmchart.MaxDecompressedFileSize,
	)

	helmchart.MaxDecompressedFileSize = 5 * 1024 * 1024
	if _, err := helmchart.Load(dir); err == nil {
		t.Fatal("expected the helm default to refuse a 6 MiB vendored file")
	}

	helmchart.MaxDecompressedFileSize = 32 * 1024 * 1024
	if _, err := helmchart.Load(dir); err != nil {
		t.Fatalf("expected the owned limit to load the chart: %v", err)
	}
}
