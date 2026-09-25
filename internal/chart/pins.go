package chart

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Dependency is one entry of a Chart.yaml dependencies list.
type Dependency struct {
	Name       string `yaml:"name"`
	Version    string `yaml:"version"`
	Repository string `yaml:"repository"`
	Alias      string `yaml:"alias,omitempty"`
	Condition  string `yaml:"condition,omitempty"`
}

// Dependencies returns the dependencies a Chart.yaml declares, in order.
func Dependencies(path string) ([]Dependency, error) {
	data, err := os.ReadFile(path) // #nosec // path comes from controlled CLI input
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var declared struct {
		Dependencies []Dependency `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal(data, &declared); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return declared.Dependencies, nil
}

// SetDependencyVersions rewrites the version of each named dependency and
// nothing else, preserving comments and field order. It reports whether the
// file changed; naming a dependency the chart does not declare is an error.
func SetDependencyVersions(path string, versions map[string]string) (bool, error) {
	data, err := os.ReadFile(path) // #nosec
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("parsing %s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return false, fmt.Errorf("%s: expected a mapping document", path)
	}
	deps := mappingValue(doc.Content[0], "dependencies")
	if deps == nil || deps.Kind != yaml.SequenceNode {
		return false, fmt.Errorf("%s: no dependencies sequence to pin", path)
	}
	remaining := make(map[string]string, len(versions))
	for name, version := range versions {
		remaining[name] = version
	}
	changed := false
	for _, entry := range deps.Content {
		nameNode := mappingValue(entry, "name")
		if nameNode == nil {
			continue
		}
		version, wanted := remaining[nameNode.Value]
		if !wanted {
			continue
		}
		versionNode := mappingValue(entry, "version")
		if versionNode == nil {
			return false, fmt.Errorf("%s: dependency %s declares no version", path, nameNode.Value)
		}
		if versionNode.Value != version {
			versionNode.Value = version
			changed = true
		}
		delete(remaining, nameNode.Value)
	}
	for name := range remaining {
		return false, fmt.Errorf("%s: declares no dependency %s", path, name)
	}
	if !changed {
		return false, nil
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return false, fmt.Errorf("marshalling %s: %w", path, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil { // #nosec
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

// mappingValue returns the value node of key in a mapping node, or nil.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
