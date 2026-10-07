// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

// Package postrender reproduces the post-rendering helm-controller applies to
// a chart before storing the release manifest (helm-controller
// internal/postrender.BuildPostRenderers):
//
//  1. each .spec.postRenderers[].kustomize entry (patches + images), in order;
//  2. .spec.commonMetadata labels / annotations, on metadata only;
//  3. the origin labels helm.toolkit.fluxcd.io/{name,namespace}, on metadata only.
//
// Without this step every resource would show a spurious label diff.
package postrender

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	"sigs.k8s.io/kustomize/api/builtins" //nolint:staticcheck // helm-controller uses the same transformers.
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/provider"
	"sigs.k8s.io/kustomize/api/resmap"
	kustypes "sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/kustomize/kyaml/resid"

	"github.com/jhaumont/flux-helm-diff/internal/flux"
)

const (
	originNameLabel      = "helm.toolkit.fluxcd.io/name"
	originNamespaceLabel = "helm.toolkit.fluxcd.io/namespace"
)

// kustomizeMutex works around concurrent map access in kustomize builds
// (kubernetes-sigs/kustomize#3659), as helm-controller does.
var kustomizeMutex sync.Mutex

// step is one post-render pass over a multi-document manifest.
type step func(in []byte) ([]byte, error)

// Renderer applies helm-controller's post-renderers in order. It implements
// helm.sh/helm/v4/pkg/postrenderer.PostRenderer, so Helm runs it itself and
// follows the post-render strategy.
type Renderer struct {
	steps []step
}

// New builds the post-renderers of h; ns is the HelmRelease namespace.
func New(h *flux.HelmRelease, ns string) *Renderer {
	r := &Renderer{}

	for _, pr := range h.Spec.PostRenderers {
		if pr.Kustomize != nil {
			k := pr.Kustomize
			r.steps = append(r.steps, func(in []byte) ([]byte, error) { return kustomize(in, k) })
		}
	}

	if cm := h.Spec.CommonMetadata; cm != nil {
		var ts []resmap.Transformer
		if cm.Labels != nil {
			ts = append(ts, labelTransformer(cm.Labels))
		}
		if cm.Annotations != nil {
			ts = append(ts, annotationTransformer(cm.Annotations))
		}
		if len(ts) > 0 {
			r.steps = append(r.steps, func(in []byte) ([]byte, error) { return transform(in, ts...) })
		}
	}

	origin := labelTransformer(map[string]string{
		originNameLabel:      h.Metadata.Name,
		originNamespaceLabel: ns,
	})
	r.steps = append(r.steps, func(in []byte) ([]byte, error) { return transform(in, origin) })
	return r
}

// Run implements helm.sh/helm/v4/pkg/postrenderer.PostRenderer.
func (r *Renderer) Run(in *bytes.Buffer) (*bytes.Buffer, error) {
	out, err := r.Render(in.Bytes())
	if err != nil {
		return nil, err
	}
	return bytes.NewBuffer(out), nil
}

// Render runs every step over a multi-document manifest.
func (r *Renderer) Render(manifest []byte) ([]byte, error) {
	if strings.TrimSpace(string(manifest)) == "" {
		return manifest, nil
	}
	cur := manifest
	for _, s := range r.steps {
		out, err := s(cur)
		if err != nil {
			return nil, err
		}
		cur = out
	}
	return cur, nil
}

// kustomize applies one .spec.postRenderers[].kustomize entry with a
// kustomize build, like helm-controller's Kustomize post-renderer.
func kustomize(in []byte, k *flux.Kustomize) ([]byte, error) {
	const input = "helm-output.yaml"

	cfg := kustypes.Kustomization{}
	cfg.APIVersion = kustypes.KustomizationVersion
	cfg.Kind = kustypes.KustomizationKind
	cfg.Resources = []string{input}
	for _, img := range k.Images {
		cfg.Images = append(cfg.Images, kustypes.Image{
			Name: img.Name, NewName: img.NewName, NewTag: img.NewTag, Digest: img.Digest,
		})
	}
	for _, p := range k.Patches {
		cfg.Patches = append(cfg.Patches, kustypes.Patch{Patch: p.Patch, Target: toSelector(p.Target)})
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	fs := filesys.MakeFsInMemory()
	if err := fs.WriteFile(input, in); err != nil {
		return nil, err
	}
	if err := fs.WriteFile("kustomization.yaml", data); err != nil {
		return nil, err
	}

	kustomizeMutex.Lock()
	defer kustomizeMutex.Unlock()
	resMap, err := krusty.MakeKustomizer(&krusty.Options{
		LoadRestrictions: kustypes.LoadRestrictionsNone,
		PluginConfig:     kustypes.DisabledPluginConfig(),
	}).Run(fs, ".")
	if err != nil {
		return nil, err
	}
	return resMap.AsYaml()
}

func toSelector(t *flux.Selector) *kustypes.Selector {
	if t == nil {
		return nil
	}
	return &kustypes.Selector{
		ResId: resid.ResId{
			Gvk:       resid.Gvk{Group: t.Group, Version: t.Version, Kind: t.Kind},
			Name:      t.Name,
			Namespace: t.Namespace,
		},
		AnnotationSelector: t.AnnotationSelector,
		LabelSelector:      t.LabelSelector,
	}
}

// transform applies kustomize transformers to a manifest without a kustomize
// build, like helm-controller's CommonRenderer and OriginLabels.
func transform(in []byte, ts ...resmap.Transformer) ([]byte, error) {
	resMap, err := resmap.NewFactory(provider.NewDefaultDepProvider().GetResourceFactory()).NewResMapFromBytes(in)
	if err != nil {
		return nil, err
	}
	for _, t := range ts {
		if err := t.Transform(resMap); err != nil {
			return nil, err
		}
	}
	return resMap.AsYaml()
}

// labelTransformer sets labels on metadata only: unlike kustomize
// commonLabels, selectors and pod templates are left untouched.
func labelTransformer(labels map[string]string) resmap.Transformer {
	return &builtins.LabelTransformerPlugin{
		Labels:     labels,
		FieldSpecs: []kustypes.FieldSpec{{Path: "metadata/labels", CreateIfNotPresent: true}},
	}
}

// annotationTransformer sets annotations on metadata only: unlike kustomize
// commonAnnotations, pod templates are left untouched.
func annotationTransformer(annotations map[string]string) resmap.Transformer {
	return &builtins.AnnotationsTransformerPlugin{
		Annotations: annotations,
		FieldSpecs:  []kustypes.FieldSpec{{Path: "metadata/annotations", CreateIfNotPresent: true}},
	}
}
