// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

package postrender

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/jhaumont/flux-helm-diff/internal/flux"
)

const deployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  selector:
    matchLabels:
      app: app
  template:
    metadata:
      labels:
        app: app
    spec:
      containers:
        - name: app
          image: example.com/app:1.0.0
`

func render(t *testing.T, h *flux.HelmRelease) map[string]any {
	t.Helper()
	out, err := New(h, "apps").Render([]byte(deployment))
	if err != nil {
		t.Fatal(err)
	}
	obj := map[string]any{}
	if err := yaml.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func get(obj map[string]any, path ...string) any {
	var cur any = obj
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func TestCommonMetadataOnlyTouchesMetadata(t *testing.T) {
	obj := render(t, &flux.HelmRelease{
		Metadata: flux.ObjectMeta{Name: "app"},
		Spec: flux.HelmReleaseSpec{CommonMetadata: &flux.CommonMetadata{
			Labels:      map[string]string{"team": "platform"},
			Annotations: map[string]string{"owner": "me"},
		}},
	})

	tests := []struct {
		path []string
		want any
	}{
		{path: []string{"metadata", "labels", "team"}, want: "platform"},
		{path: []string{"metadata", "annotations", "owner"}, want: "me"},
		{path: []string{"metadata", "labels", originNameLabel}, want: "app"},
		{path: []string{"metadata", "labels", originNamespaceLabel}, want: "apps"},
		{path: []string{"spec", "template", "metadata", "labels", "team"}, want: nil},
		{path: []string{"spec", "template", "metadata", "labels", originNameLabel}, want: nil},
		{path: []string{"spec", "template", "metadata", "annotations"}, want: nil},
		{path: []string{"spec", "selector", "matchLabels", "team"}, want: nil},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.path, "."), func(t *testing.T) {
			if got := get(obj, tt.path...); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKustomizePostRenderer(t *testing.T) {
	obj := render(t, &flux.HelmRelease{
		Metadata: flux.ObjectMeta{Name: "app"},
		Spec: flux.HelmReleaseSpec{PostRenderers: []flux.PostRenderer{{Kustomize: &flux.Kustomize{
			Images: []flux.Image{{Name: "example.com/app", NewTag: "2.0.0"}},
			Patches: []flux.Patch{{
				Target: &flux.Selector{Kind: "Deployment"},
				Patch:  `[{"op": "add", "path": "/spec/replicas", "value": 3}]`,
			}},
		}}}},
	})

	containers, _ := get(obj, "spec", "template", "spec", "containers").([]any)
	if len(containers) != 1 || get(containers[0].(map[string]any), "image") != "example.com/app:2.0.0" {
		t.Errorf("image not replaced: %v", containers)
	}
	if got := get(obj, "spec", "replicas"); got != float64(3) {
		t.Errorf("patch not applied: replicas = %v", got)
	}
}
