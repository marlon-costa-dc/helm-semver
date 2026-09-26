// Package catalog records published chart versions in a channel catalog.
//
// The catalog is the YAML a GitOps channel reads to pin every chart it deploys
// (global.charts.releases.<chart>). Only a release writes it, and only with what
// the registry confirmed: the version pushed, the manifest digest the registry
// answered, and the release commit that produced it.
package catalog

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Entry is one chart's published release in the catalog.
type Entry struct {
	Version string
	Digest  string
	Repo    string
	Commit  string
}

// Record writes chart's entry into the catalog at path, replacing any earlier
// entry of that chart and leaving every other chart and comment untouched. The
// catalog must already exist: its location belongs to the channel, and a
// missing file means the release was pointed at the wrong checkout.
func Record(path, chart string, entry Entry) error {
	doc, err := readCatalog(path)
	if err != nil {
		return err
	}
	releases := ensureMapping(ensureMapping(ensureMapping(doc.Content[0], "global"), "charts"), "releases")
	setMapping(releases, chart, entryNode(entry))
	return writeCatalog(path, doc)
}

// RecordCluster writes the version one cluster of the channel runs for chart,
// under releases.<chart>.clusters.<cluster>. The channel entry must exist: a
// cluster override refines a channel version, it never stands alone.
func RecordCluster(path, chart, cluster string, entry Entry) error {
	doc, err := readCatalog(path)
	if err != nil {
		return err
	}
	releases := ensureMapping(ensureMapping(ensureMapping(doc.Content[0], "global"), "charts"), "releases")
	channel := mappingValue(releases, chart)
	if channel == nil {
		return fmt.Errorf("catalog %s: %s has no channel entry to refine for %s", path, chart, cluster)
	}
	setMapping(ensureMapping(channel, "clusters"), cluster, entryNode(entry))
	return writeCatalog(path, doc)
}

func readCatalog(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path) // #nosec // path comes from controlled CLI input
	if err != nil {
		return nil, fmt.Errorf("reading catalog %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing catalog %s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("catalog %s: expected a mapping document", path)
	}
	return &doc, nil
}

// entryNode renders an entry; empty receipt fields are omitted, never written
// as empty strings.
func entryNode(entry Entry) *yaml.Node {
	value := &yaml.Node{Kind: yaml.MappingNode}
	for _, field := range [][2]string{
		{"version", entry.Version}, {"digest", entry.Digest}, {"repo", entry.Repo}, {"commit", entry.Commit},
	} {
		if field[1] == "" {
			continue
		}
		value.Content = append(value.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: field[0]},
			&yaml.Node{Kind: yaml.ScalarNode, Value: field[1]},
		)
	}
	return value
}

func writeCatalog(path string, doc *yaml.Node) error {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshalling catalog %s: %w", path, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil { // #nosec
		return fmt.Errorf("writing catalog %s: %w", path, err)
	}
	return nil
}

// mappingValue returns the value of key in a mapping node, or nil.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// Entries reads every chart entry the catalog at path records.
func Entries(path string) (map[string]Entry, error) {
	data, err := os.ReadFile(path) // #nosec // path comes from controlled CLI input
	if err != nil {
		return nil, fmt.Errorf("reading catalog %s: %w", path, err)
	}
	var doc struct {
		Global struct {
			Charts struct {
				Releases map[string]struct {
					Version string `yaml:"version"`
					Digest  string `yaml:"digest"`
					Repo    string `yaml:"repo"`
					Commit  string `yaml:"commit"`
				} `yaml:"releases"`
			} `yaml:"charts"`
		} `yaml:"global"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing catalog %s: %w", path, err)
	}
	entries := make(map[string]Entry, len(doc.Global.Charts.Releases))
	for name, raw := range doc.Global.Charts.Releases {
		entries[name] = Entry{Version: raw.Version, Digest: raw.Digest, Repo: raw.Repo, Commit: raw.Commit}
	}
	return entries, nil
}

// ensureMapping returns the mapping under key, creating it when absent. Every
// mapping the catalog writes into is block style, so an empty `releases: {}`
// grows into a reviewable file, not one flow line.
func ensureMapping(node *yaml.Node, key string) *yaml.Node {
	node.Style &^= yaml.FlowStyle
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1].Style &^= yaml.FlowStyle
			return node.Content[i+1]
		}
	}
	child := &yaml.Node{Kind: yaml.MappingNode}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, child)
	return child
}

// setMapping replaces key's value in a mapping, or appends it.
func setMapping(node *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
}
