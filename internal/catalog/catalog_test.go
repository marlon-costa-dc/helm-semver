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

func TestEntries_ReadsWhatRecordWrote(t *testing.T) {
	path := writeFile(t, "global: {}\n")
	want := Entry{Version: "1.2.3", Digest: "sha256:d", Repo: "acme/charts", Commit: "abc"}
	if err := Record(path, "web", want); err != nil {
		t.Fatalf("recording: %v", err)
	}
	got, err := Entries(path)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if got["web"] != want || len(got) != 1 {
		t.Errorf("entries = %+v, want web=%+v", got, want)
	}
	if _, err := Entries(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("a missing catalog was read")
	}
	if _, err := Entries(writeFile(t, "global: [\n")); err == nil {
		t.Error("an unparsable catalog was read")
	}
}

func TestRecordCluster_RefinesTheChannelEntryForOneCluster(t *testing.T) {
	path := writeFile(t, "global: {}\n")
	if err := Record(path, "web", Entry{Version: "1.0.0", Digest: "sha256:a", Repo: "acme/charts"}); err != nil {
		t.Fatalf("recording channel: %v", err)
	}
	if err := RecordCluster(path, "web", "dc-prod", Entry{Version: "0.9.0", Digest: "sha256:b"}); err != nil {
		t.Fatalf("recording cluster: %v", err)
	}
	got := read(t, path)
	for _, want := range []string{"version: 1.0.0", "clusters:", "dc-prod:", "version: 0.9.0", "digest: sha256:b"} {
		if !strings.Contains(got, want) {
			t.Errorf("catalog lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "commit:") {
		t.Errorf("an empty receipt field was written:\n%s", got)
	}
	if err := Record(path, "web", Entry{Version: "1.1.0", Digest: "sha256:c"}); err != nil {
		t.Fatalf("re-recording channel: %v", err)
	}
	if got := read(t, path); strings.Contains(got, "clusters:") {
		t.Errorf("a new channel version kept the cluster overrides:\n%s", got)
	}
}

func TestRecordCluster_RefusesAChartWithoutAChannelEntry(t *testing.T) {
	path := writeFile(t, "global: {}\n")
	if err := RecordCluster(path, "web", "dc-prod", Entry{Version: "0.9.0"}); err == nil {
		t.Error("a cluster override without a channel entry was accepted")
	}
	if err := RecordCluster(filepath.Join(t.TempDir(), "absent.yaml"), "web", "dc-prod", Entry{}); err == nil {
		t.Error("a missing catalog was accepted")
	}
}
