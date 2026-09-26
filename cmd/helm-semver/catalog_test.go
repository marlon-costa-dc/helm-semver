package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rhysmcneill/helm-semver/internal/catalog"
	igit "github.com/rhysmcneill/helm-semver/internal/git"
)

func TestCatalogSet_RecordsAPublishedVersionForTheChannelAndOneCluster(t *testing.T) {
	// Given a chart published at 0.1.0 and 0.2.0, released only at 0.2.0 here.
	publisher := ociRegistry(t)
	cr := newChartRepo(t, "web")
	for _, version := range []string{"0.1.0", "0.2.0"} {
		dir := filepath.Join(t.TempDir(), "web")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte("apiVersion: v2\nname: web\nversion: "+version+"\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := publisher.Push(dir, version); err != nil {
			t.Fatalf("push %s: %v", version, err)
		}
	}
	gitClient, err := igit.Open(cr.root)
	if err != nil {
		t.Fatalf("git: %v", err)
	}
	path := writeCatalog(t)
	var out bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&out)

	// When the channel adopts 0.1.0 (tagged) and dc-prod keeps 0.2.0.
	opts := &catalogSetOptions{catalog: path, chart: "web", version: "0.1.0", githubOwner: "acme", githubRepo: "charts"}
	if err := catalogSet(command, opts, gitClient, publisher); err != nil {
		t.Fatalf("channel: %v", err)
	}
	opts.version, opts.cluster = "0.2.0", "dc-prod"
	if err := catalogSet(command, opts, gitClient, publisher); err != nil {
		t.Fatalf("cluster: %v", err)
	}

	// Then both carry the registry digest; the tagged channel version its commit.
	entries, err := catalog.Entries(path)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if e := entries["web"]; e.Version != "0.1.0" || !strings.HasPrefix(e.Digest, "sha256:") || e.Commit == "" || e.Repo != "acme/charts" {
		t.Errorf("channel entry = %+v", e)
	}
	data, _ := os.ReadFile(filepath.Clean(path))
	if !strings.Contains(string(data), "dc-prod:") || !strings.Contains(string(data), "version: 0.2.0") {
		t.Errorf("cluster override missing:\n%s", data)
	}
}

func TestCatalogSet_RefusesAVersionTheRegistryDoesNotHold(t *testing.T) {
	publisher := ociRegistry(t)
	cr := newChartRepo(t, "web")
	gitClient, err := igit.Open(cr.root)
	if err != nil {
		t.Fatalf("git: %v", err)
	}
	opts := &catalogSetOptions{catalog: writeCatalog(t), chart: "web", version: "9.9.9", githubOwner: "acme", githubRepo: "charts"}
	if err := catalogSet(&cobra.Command{}, opts, gitClient, publisher); err == nil {
		t.Error("an unpublished version was recorded")
	}
}
