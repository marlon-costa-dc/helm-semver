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
	data, err := os.ReadFile(path) // #nosec // path comes from controlled CLI input
	if err != nil {
		return fmt.Errorf("reading catalog %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing catalog %s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("catalog %s: expected a mapping document", path)
	}
	releases := ensureMapping(ensureMapping(ensureMapping(doc.Content[0], "global"), "charts"), "releases")
	value := &yaml.Node{Kind: yaml.MappingNode}
	for _, field := range [][2]string{
		{"version", entry.Version}, {"digest", entry.Digest}, {"repo", entry.Repo}, {"commit", entry.Commit},
	} {
		value.Content = append(value.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: field[0]},
			&yaml.Node{Kind: yaml.ScalarNode, Value: field[1]},
		)
	}
	setMapping(releases, chart, value)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshalling catalog %s: %w", path, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil { // #nosec
		return fmt.Errorf("writing catalog %s: %w", path, err)
	}
	return nil
}

// ensureMapping returns the mapping under key, creating it when absent.
func ensureMapping(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
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
