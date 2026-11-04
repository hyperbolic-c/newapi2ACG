package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// LoadDir reads configuration documents, excluding mapping golden fixtures.
// Mapping DSL compilation is a later phase; unimplemented mapping files fail
// explicitly rather than being silently omitted from the configuration hash.
func LoadDir(dir string) (*Bundle, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	if err := readConfigTree(root, ".", map[string]bool{}, files); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("configuration directory contains no YAML documents")
	}
	return LoadFiles(files)
}

// readConfigTree supports Kubernetes projected-volume symlinks while keeping
// reads within the resolved configuration root. Hidden generation directories
// are reached only through their public links, never loaded a second time.
func readConfigTree(root, relative string, ancestors map[string]bool, files map[string][]byte) error {
	logical := filepath.Join(root, relative)
	resolved, err := filepath.EvalSymlinks(logical)
	if err != nil {
		return fmt.Errorf("%s: cannot resolve configuration path", relative)
	}
	inside, err := filepath.Rel(root, resolved)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s: configuration path escapes root", relative)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("%s: cannot inspect configuration path", relative)
	}
	if info.IsDir() {
		if ancestors[resolved] {
			return fmt.Errorf("%s: configuration symlink cycle", relative)
		}
		ancestors[resolved] = true
		defer delete(ancestors, resolved)
		entries, err := os.ReadDir(resolved)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") || (relative == "." && entry.Name() == "tests") {
				continue
			}
			next := filepath.Join(relative, entry.Name())
			if strings.HasSuffix(next, ".local.yaml") {
				continue
			}
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && path.Ext(next) != ".yaml" && path.Ext(next) != ".yml" {
				continue
			}
			if err := readConfigTree(root, next, ancestors, files); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: regular configuration file required", relative)
	}
	if path.Ext(relative) != ".yaml" && path.Ext(relative) != ".yml" {
		return nil
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("%s: cannot read configuration", relative)
	}
	files[filepath.ToSlash(relative)] = data
	return nil
}

// LoadFiles hashes normalized documents in sorted filename order. Environment
// variable values are never read or included in the configuration hash.
func LoadFiles(files map[string][]byte) (*Bundle, error) {
	b := &Bundle{Jobs: map[string]Job{}, Sources: map[string]Source{}, Instances: map[string]Instance{}, Targets: map[string]Target{}, Lookups: map[string]map[string]any{}}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	normalized := map[string]any{}
	for _, name := range names {
		parts := strings.Split(name, "/")
		if len(parts) != 2 || parts[1] == "" || path.Clean(name) != name {
			return nil, fmt.Errorf("%s: expected category/name.yaml", name)
		}
		ext := path.Ext(name)
		if ext != ".yaml" && ext != ".yml" {
			return nil, fmt.Errorf("%s: YAML extension required", name)
		}
		id := strings.TrimSuffix(parts[1], ext)
		if id == "" {
			return nil, fmt.Errorf("%s: empty configuration name", name)
		}
		node, err := parseYAML(name, files[name])
		if err != nil {
			return nil, err
		}
		var value any
		switch parts[0] {
		case "jobs":
			if _, exists := b.Jobs[id]; exists {
				return nil, fmt.Errorf("%s: duplicate job name", name)
			}
			job := defaultJob()
			if err := decodeStrict(name, node, &job); err != nil {
				return nil, err
			}
			if err := job.Validate(); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			b.Jobs[id] = job
			value = job
		case "sources":
			if _, exists := b.Sources[id]; exists {
				return nil, fmt.Errorf("%s: duplicate source name", name)
			}
			var source Source
			if err := decodeStrict(name, node, &source); err != nil {
				return nil, err
			}
			if err := source.Validate(); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if source.Project != id {
				return nil, fmt.Errorf("%s: project must match filename", name)
			}
			b.Sources[id] = source
			value = source
		case "instances":
			if _, exists := b.Instances[id]; exists {
				return nil, fmt.Errorf("%s: duplicate instance name", name)
			}
			var instance Instance
			if err := decodeStrict(name, node, &instance); err != nil {
				return nil, err
			}
			if err := instance.Validate(); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if instance.Instance != id {
				return nil, fmt.Errorf("%s: instance must match filename", name)
			}
			b.Instances[id] = instance
			value = instance
		case "targets":
			if _, exists := b.Targets[id]; exists {
				return nil, fmt.Errorf("%s: duplicate target name", name)
			}
			var target Target
			if err := decodeStrict(name, node, &target); err != nil {
				return nil, err
			}
			if err := target.Validate(); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if target.Target != id {
				return nil, fmt.Errorf("%s: target must match filename", name)
			}
			b.Targets[id] = target
			value = target
		case "lookups":
			if _, exists := b.Lookups[id]; exists {
				return nil, fmt.Errorf("%s: duplicate lookup name", name)
			}
			value, err = normalizeNode(node)
			if err != nil {
				return nil, fmt.Errorf("%s: invalid lookup", name)
			}
			m, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s: lookup must be a mapping", name)
			}
			b.Lookups[id] = m
		default:
			return nil, fmt.Errorf("%s: unsupported configuration category", name)
		}
		normalized[name] = value
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("configuration cannot be canonicalized")
	}
	sum := sha256.Sum256(data)
	b.Hash = hex.EncodeToString(sum[:])
	return b, nil
}

func parseYAML(file string, data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: invalid YAML", file)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: mapping document required", file)
	}
	if err := dec.Decode(new(yaml.Node)); err != io.EOF {
		return nil, fmt.Errorf("%s: exactly one YAML document required", file)
	}
	if err := checkNode(doc.Content[0], ""); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return doc.Content[0], nil
}
func checkNode(n *yaml.Node, p string) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return fmt.Errorf("%s: YAML anchors and aliases are not supported", p)
	}
	if n.Tag == "!!null" {
		return fmt.Errorf("%s: null configuration is not allowed", p)
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode || k.Tag == "!!merge" {
				return fmt.Errorf("%s: scalar keys required; YAML merge is not supported", p)
			}
			key := k.Value
			if seen[key] {
				return fmt.Errorf("%s: duplicate key", joinPath(p, key))
			}
			seen[key] = true
			if err := checkNode(v, joinPath(p, key)); err != nil {
				return err
			}
		}
	} else {
		for i, child := range n.Content {
			if err := checkNode(child, fmt.Sprintf("%s[%d]", p, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
func joinPath(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}
func decodeStrict(file string, n *yaml.Node, out any) error {
	if err := checkFields(n, reflect.TypeOf(out).Elem(), ""); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if err := n.Decode(out); err != nil {
		return fmt.Errorf("%s: invalid field type", file)
	}
	return nil
}
func checkFields(n *yaml.Node, t reflect.Type, p string) error {
	if t.Kind() == reflect.Interface {
		return nil
	}
	if t.Kind() == reflect.Struct {
		if n.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: mapping required", p)
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			name := strings.Split(field.Tag.Get("yaml"), ",")[0]
			fields[name] = field.Type
		}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i].Value
			ft, ok := fields[key]
			if !ok {
				return fmt.Errorf("%s: unknown field", joinPath(p, key))
			}
			if err := checkFields(n.Content[i+1], ft, joinPath(p, key)); err != nil {
				return err
			}
		}
	} else if t.Kind() == reflect.Map {
		if n.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: mapping required", p)
		}
		for i := 0; i < len(n.Content); i += 2 {
			if err := checkFields(n.Content[i+1], t.Elem(), joinPath(p, n.Content[i].Value)); err != nil {
				return err
			}
		}
	} else if t.Kind() == reflect.Slice {
		if n.Kind != yaml.SequenceNode {
			return fmt.Errorf("%s: list required", p)
		}
		for i, c := range n.Content {
			if err := checkFields(c, t.Elem(), fmt.Sprintf("%s[%d]", p, i)); err != nil {
				return err
			}
		}
	} else {
		expected := "!!str"
		switch t.Kind() {
		case reflect.Int:
			expected = "!!int"
		case reflect.Bool:
			expected = "!!bool"
		}
		if n.Kind != yaml.ScalarNode || n.Tag != expected {
			return fmt.Errorf("%s: incorrect scalar type", p)
		}
	}
	return nil
}
func normalizeNode(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.MappingNode:
		m := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			v, err := normalizeNode(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m[n.Content[i].Value] = v
		}
		return m, nil
	case yaml.SequenceNode:
		a := []any{}
		for _, c := range n.Content {
			v, err := normalizeNode(c)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		return a, nil
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!str":
			return n.Value, nil
		case "!!bool":
			return strconv.ParseBool(n.Value)
		case "!!int":
			var value int64
			if err := n.Decode(&value); err != nil {
				return nil, err
			}
			return value, nil
		case "!!float":
			var value float64
			if err := n.Decode(&value); err != nil {
				return nil, err
			}
			return value, nil
		}
	}
	return nil, fmt.Errorf("unsupported YAML value")
}
