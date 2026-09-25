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

func TestPromote_CarriesTheValidatedVersionAndDigestToTheTargetCatalog(t *testing.T) {
	// Given charts released into the develop catalog of a real registry.
	publisher := ociRegistry(t)
	isolateHelm(t)
	cr := hierarchyRepo(t, publisher.RegistryURL)
	develop := writeCatalog(t)
	main := writeCatalog(t)
	var stdout, stderr bytes.Buffer
	if err := cr.runner(t, &releaseOptions{
		registry: publisher.RegistryURL, registryType: "oci", catalog: develop,
		githubOwner: "acme", githubRepo: "charts", dependencyBuild: true,
	}, publisher, &stdout, &stderr).run(); err != nil {
		t.Fatalf("release: %v\n%s", err, stderr.String())
	}
	gitClient, err := igit.Open(cr.root)
	if err != nil {
		t.Fatalf("opening git: %v", err)
	}
	opts := &promoteOptions{chartsDir: "charts", from: develop, to: main, githubOwner: "acme", githubRepo: "charts"}
	command := &cobra.Command{}
	var out bytes.Buffer
	command.SetOut(&out)

	// When the branch is promoted.
	if err := promote(command, opts, cr.root, gitClient, publisher); err != nil {
		t.Fatalf("promote: %v", err)
	}

	// Then the main catalog holds exactly what develop validated.
	validated, err := catalog.Entries(develop)
	if err != nil {
		t.Fatalf("reading develop: %v", err)
	}
	promoted, err := catalog.Entries(main)
	if err != nil {
		t.Fatalf("reading main: %v", err)
	}
	for _, name := range []string{"lib", "app"} {
		if promoted[name] != validated[name] {
			t.Errorf("%s promoted %+v, develop validated %+v", name, promoted[name], validated[name])
		}
	}

	// And a second promotion changes nothing.
	before, _ := os.ReadFile(filepath.Clean(main))
	out.Reset()
	if err := promote(command, opts, cr.root, gitClient, publisher); err != nil {
		t.Fatalf("second promote: %v", err)
	}
	after, _ := os.ReadFile(filepath.Clean(main))
	if !bytes.Equal(before, after) || out.Len() != 0 {
		t.Errorf("a repeated promotion rewrote the catalog:\n%s", out.String())
	}
}

func TestPromote_RefusesADigestTheSourceChannelDidNotValidate(t *testing.T) {
	// Given a develop catalog whose digest for the released version differs from the registry's.
	publisher := ociRegistry(t)
	isolateHelm(t)
	cr := hierarchyRepo(t, publisher.RegistryURL)
	develop := writeCatalog(t)
	var stdout, stderr bytes.Buffer
	if err := cr.runner(t, &releaseOptions{
		registry: publisher.RegistryURL, registryType: "oci", catalog: develop,
		githubOwner: "acme", githubRepo: "charts", dependencyBuild: true,
	}, publisher, &stdout, &stderr).run(); err != nil {
		t.Fatalf("release: %v\n%s", err, stderr.String())
	}
	if err := catalog.Record(develop, "lib", catalog.Entry{Version: "0.2.0", Digest: "sha256:tampered"}); err != nil {
		t.Fatalf("tampering: %v", err)
	}
	gitClient, err := igit.Open(cr.root)
	if err != nil {
		t.Fatalf("opening git: %v", err)
	}

	// When the branch is promoted.
	err = promote(&cobra.Command{}, &promoteOptions{chartsDir: "charts", from: develop, to: writeCatalog(t)}, cr.root, gitClient, publisher)

	// Then the promotion is refused naming the chart.
	if err == nil || !strings.Contains(err.Error(), "lib 0.2.0") {
		t.Fatalf("promote error = %v, want the lib digest mismatch", err)
	}
}
