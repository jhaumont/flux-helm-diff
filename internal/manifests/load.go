// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

// Package manifests loads the local objects the user is about to deploy:
// HelmReleases plus the objects they may depend on (ConfigMaps / Secrets used
// in valuesFrom, HelmRepositories, OCIRepositories, HelmCharts). Local objects
// take precedence over their in-cluster counterparts, so a change to a values
// ConfigMap in the same commit shows up in the diff.
package manifests

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/jhaumont/flux-helm-diff/internal/flux"
)

// Set is an index of the local objects, keyed by "<namespace>/<name>".
type Set struct {
	HelmReleases     []*flux.HelmRelease
	ConfigMaps       map[string]*corev1.ConfigMap
	Secrets          map[string]*corev1.Secret
	HelmRepositories map[string]*flux.HelmRepository
	OCIRepositories  map[string]*flux.OCIRepository
	HelmCharts       map[string]*flux.HelmChart

	// Warnings collected while loading (e.g. SOPS-encrypted secrets skipped).
	Warnings []string
}

func Key(ns, name string) string { return ns + "/" + name }

func newSet() *Set {
	return &Set{
		ConfigMaps:       map[string]*corev1.ConfigMap{},
		Secrets:          map[string]*corev1.Secret{},
		HelmRepositories: map[string]*flux.HelmRepository{},
		OCIRepositories:  map[string]*flux.OCIRepository{},
		HelmCharts:       map[string]*flux.HelmChart{},
	}
}

// Load reads files, directories (recursively, *.yaml|*.yml) or "-" (stdin).
func Load(paths []string, defaultNS string, stdin io.Reader) (*Set, error) {
	s := newSet()
	for _, p := range paths {
		if p == "-" {
			if err := s.load(stdin, "<stdin>", defaultNS, false); err != nil {
				return nil, err
			}
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if err := s.loadFile(p, defaultNS, false); err != nil {
				return nil, err
			}
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != p && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".yaml", ".yml":
				// A directory may hold files that are not Kubernetes
				// manifests, such as chart templates: skip them.
				return s.loadFile(path, defaultNS, true)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Set) loadFile(path, defaultNS string, skipInvalid bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return s.load(f, path, defaultNS, skipInvalid)
}

// load decodes every document of r, then indexes them. A file that cannot be
// parsed is skipped as a whole, with a warning, when skipInvalid is set.
func (s *Set) load(r io.Reader, src, defaultNS string, skipInvalid bool) error {
	objs, err := decode(r)
	if err != nil {
		if skipInvalid {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s: skipped, not a valid YAML manifest: %v", src, err))
			return nil
		}
		return fmt.Errorf("%s: %w", src, err)
	}
	for _, obj := range objs {
		if err := s.add(obj, src, defaultNS); err != nil {
			return fmt.Errorf("%s: %w", src, err)
		}
	}
	return nil
}

func decode(r io.Reader) ([]map[string]any, error) {
	dec := k8syaml.NewYAMLOrJSONDecoder(r, 4096)
	var objs []map[string]any
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				return objs, nil
			}
			return nil, err
		}
		if len(obj) > 0 {
			objs = append(objs, obj)
		}
	}
}

func (s *Set) add(obj map[string]any, src, defaultNS string) error {
	apiVersion, _ := obj["apiVersion"].(string)
	kind, _ := obj["kind"].(string)
	group := ""
	if i := strings.Index(apiVersion, "/"); i > 0 {
		group = apiVersion[:i]
	}

	raw, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	nsOf := func(ns string) string {
		if ns == "" {
			return defaultNS
		}
		return ns
	}

	switch {
	case group == "helm.toolkit.fluxcd.io" && kind == "HelmRelease":
		hr := &flux.HelmRelease{}
		if err := json.Unmarshal(raw, hr); err != nil {
			return fmt.Errorf("decoding HelmRelease: %w", err)
		}
		hr.Metadata.Namespace = nsOf(hr.Metadata.Namespace)
		hr.Status = flux.HelmReleaseStatus{} // never trust a status from Git
		hr.Source = src
		s.HelmReleases = append(s.HelmReleases, hr)

	case apiVersion == "v1" && kind == "ConfigMap":
		cm := &corev1.ConfigMap{}
		if err := json.Unmarshal(raw, cm); err != nil {
			return fmt.Errorf("decoding ConfigMap: %w", err)
		}
		s.ConfigMaps[Key(nsOf(cm.Namespace), cm.Name)] = cm

	case apiVersion == "v1" && kind == "Secret":
		if _, encrypted := obj["sops"]; encrypted {
			s.Warnings = append(s.Warnings,
				fmt.Sprintf("%s: Secret is SOPS-encrypted, the in-cluster version will be used", src))
			return nil
		}
		sec := &corev1.Secret{}
		if err := json.Unmarshal(raw, sec); err != nil {
			return fmt.Errorf("decoding Secret: %w", err)
		}
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		for k, v := range sec.StringData {
			sec.Data[k] = []byte(v)
		}
		s.Secrets[Key(nsOf(sec.Namespace), sec.Name)] = sec

	case group == "source.toolkit.fluxcd.io" && kind == "HelmRepository":
		o := &flux.HelmRepository{}
		if err := json.Unmarshal(raw, o); err != nil {
			return fmt.Errorf("decoding HelmRepository: %w", err)
		}
		s.HelmRepositories[Key(nsOf(o.Metadata.Namespace), o.Metadata.Name)] = o

	case group == "source.toolkit.fluxcd.io" && kind == "OCIRepository":
		o := &flux.OCIRepository{}
		if err := json.Unmarshal(raw, o); err != nil {
			return fmt.Errorf("decoding OCIRepository: %w", err)
		}
		s.OCIRepositories[Key(nsOf(o.Metadata.Namespace), o.Metadata.Name)] = o

	case group == "source.toolkit.fluxcd.io" && kind == "HelmChart":
		o := &flux.HelmChart{}
		if err := json.Unmarshal(raw, o); err != nil {
			return fmt.Errorf("decoding HelmChart: %w", err)
		}
		s.HelmCharts[Key(nsOf(o.Metadata.Namespace), o.Metadata.Name)] = o
	}
	return nil
}
