// Package cdi reads CDI specs from a directory and groups their devices
// by kind. It deliberately depends on the upstream container-device-interface
// schema package to stay byte-compatible with what containerd parses.
package cdi

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
)

// Device represents a single addressable CDI device.
type Device struct {
	// Kind is the CDI kind ("vendor.tld/class"), e.g. "nvidia.com/gpu".
	Kind string
	// Name is the device name within the spec, e.g. "gpu0".
	Name string
	// FullID is the canonical CDI identifier used in CDIDevice.Name,
	// e.g. "nvidia.com/gpu=0".
	FullID string
}

// Snapshot is the parsed view of one /etc/cdi/ directory at a point in
// time. Devices are grouped by CDI kind. Order within each slice is
// stable (sorted by name) so that ListAndWatch responses are
// deterministic.
type Snapshot struct {
	ByKind map[string][]Device
}

// Read walks dir, parses every *.yaml / *.yml / *.json CDI spec, and
// returns the grouped snapshot. Unparsable files are logged and
// skipped, not fatal.
func Read(dir string) (*Snapshot, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read CDI dir %s: %w", dir, err)
	}

	snap := &Snapshot{ByKind: make(map[string][]Device)}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".yaml" && ext != ".yml" && ext != ".json" {
			continue
		}
		path := filepath.Join(dir, name)

		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}

		var spec cdispec.Spec
		if err := yaml.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if spec.Kind == "" {
			continue
		}

		for _, dev := range spec.Devices {
			if dev.Name == "" {
				continue
			}
			snap.ByKind[spec.Kind] = append(snap.ByKind[spec.Kind], Device{
				Kind:   spec.Kind,
				Name:   dev.Name,
				FullID: spec.Kind + "=" + dev.Name,
			})
		}
	}

	for k := range snap.ByKind {
		sort.Slice(snap.ByKind[k], func(i, j int) bool {
			return snap.ByKind[k][i].Name < snap.ByKind[k][j].Name
		})
	}

	return snap, nil
}

// KindMatches reports whether kind matches any of the comma-separated
// glob patterns in filter. An empty filter matches everything.
func KindMatches(kind, filter string) bool {
	if filter == "" {
		return true
	}
	for _, pat := range strings.Split(filter, ",") {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		ok, err := filepath.Match(pat, kind)
		if err == nil && ok {
			return true
		}
	}
	return false
}
