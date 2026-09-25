package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "releases.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing catalog: %v", err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("reading catalog: %v", err)
	}
	return string(data)
}

func TestRecord_ReplacesOnlyTheChartsEntryAndKeepsTheRest(t *testing.T) {
	// Given a catalog holding two charts and a comment.
	path := writeFile(t, "# channel catalog\nglobal:\n  charts:\n    releases:\n"+
		"      web:\n        version: 0.1.0\n        digest: sha256:old\n"+
		"      api:\n        version: 1.0.0\n        digest: sha256:api\n")

	// When web is recorded at a new version.
	err := Record(path, "web", Entry{Version: "0.2.0", Digest: "sha256:new", Repo: "acme/charts", Commit: "abc"})
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	// Then web carries the new entry, api and the comment are untouched.
	got := read(t, path)
	for _, want := range []string{"# channel catalog", "version: 0.2.0", "digest: sha256:new", "repo: acme/charts", "commit: abc", "version: 1.0.0", "digest: sha256:api"} {
		if !strings.Contains(got, want) {
			t.Errorf("catalog lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sha256:old") {
		t.Errorf("the replaced entry survived:\n%s", got)
	}
}

func TestRecord_CreatesTheReleasesPathInAnEmptyCatalog(t *testing.T) {
	path := writeFile(t, "global: {}\n")
	if err := Record(path, "web", Entry{Version: "0.1.0", Digest: "sha256:d"}); err != nil {
		t.Fatalf("recording: %v", err)
	}
	if got := read(t, path); !strings.Contains(got, "releases:") || !strings.Contains(got, "web:") {
		t.Errorf("releases.web not created:\n%s", got)
	}
}

func TestRecord_RefusesAMissingOrNonMappingCatalog(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	if err := Record(missing, "web", Entry{}); err == nil {
		t.Error("a missing catalog was accepted")
	}
	if err := Record(writeFile(t, "- a\n- b\n"), "web", Entry{}); err == nil {
		t.Error("a sequence document was accepted as a catalog")
	}
	if err := Record(writeFile(t, "global: [\n"), "web", Entry{}); err == nil {
		t.Error("an unparsable catalog was accepted")
	}
}
