// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	kubefake "helm.sh/helm/v4/pkg/kube/fake"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"

	"github.com/jhaumont/flux-helm-diff/internal/flux"
)

func testConfig() *action.Configuration {
	cfg := action.NewConfiguration()
	cfg.Releases = storage.Init(driver.NewMemory())
	cfg.KubeClient = &kubefake.PrintingKubeClient{Out: io.Discard, LogOutput: io.Discard}
	cfg.Capabilities = common.DefaultCapabilities
	return cfg
}

func testChart() *chart.Chart {
	return &chart.Chart{
		Metadata: &chart.Metadata{APIVersion: "v2", Name: "demo", Version: "1.0.0"},
		Values:   map[string]any{"replicas": 1},
		Templates: []*common.File{
			{Name: "templates/deploy.yaml", Data: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  replicas: {{ .Values.replicas }}
  template:
    spec:
      containers:
        - name: app
          image: example.com/app:1.0.0
`)},
			{Name: "templates/hook.yaml", Data: []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: demo-hook
  annotations:
    helm.sh/hook: pre-install,pre-upgrade
data: {}
`)},
		},
	}
}

func testHelmRelease(strategy string) *flux.HelmRelease {
	return &flux.HelmRelease{
		Metadata: flux.ObjectMeta{Name: "demo", Namespace: "apps"},
		Spec: flux.HelmReleaseSpec{
			PostRenderStrategy: strategy,
			CommonMetadata:     &flux.CommonMetadata{Labels: map[string]string{"team": "platform"}},
			PostRenderers: []flux.PostRenderer{{Kustomize: &flux.Kustomize{
				Images: []flux.Image{{Name: "example.com/app", NewTag: "2.0.0"}},
			}}},
		},
	}
}

func TestRenderAppliesPostRenderers(t *testing.T) {
	tests := []struct {
		strategy         string
		hookPostRendered bool
	}{
		{strategy: "", hookPostRendered: true},
		{strategy: "combined", hookPostRendered: true},
		{strategy: "separate", hookPostRendered: true},
		{strategy: "nohooks", hookPostRendered: false},
	}
	for _, tt := range tests {
		t.Run("strategy="+tt.strategy, func(t *testing.T) {
			e := &Engine{Opts: Options{DryRun: string(action.DryRunServer)}}
			h := testHelmRelease(tt.strategy)
			ref := flux.DesiredRelease(h, "flux-system")

			prs, err := e.postRenderStrategy(h)
			if err != nil {
				t.Fatal(err)
			}
			rel, err := e.render(context.Background(), testConfig(), h, "apps", ref, testChart(),
				map[string]any{"replicas": 3}, modeInstall, prs)
			if err != nil {
				t.Fatal(err)
			}

			for _, want := range []string{
				"replicas: 3",
				"image: example.com/app:2.0.0",
				"team: platform",
				"helm.toolkit.fluxcd.io/name: demo",
				"helm.toolkit.fluxcd.io/namespace: apps",
			} {
				if !strings.Contains(rel.Manifest, want) {
					t.Errorf("manifest does not contain %q:\n%s", want, rel.Manifest)
				}
			}

			if len(rel.Hooks) != 1 {
				t.Fatalf("expected 1 hook, got %d", len(rel.Hooks))
			}
			got := strings.Contains(rel.Hooks[0].Manifest, "helm.toolkit.fluxcd.io/name: demo")
			if got != tt.hookPostRendered {
				t.Errorf("hook post-rendered = %v, want %v:\n%s", got, tt.hookPostRendered, rel.Hooks[0].Manifest)
			}
		})
	}
}

func TestAssemble(t *testing.T) {
	e := &Engine{Opts: Options{DryRun: string(action.DryRunServer)}}
	h := testHelmRelease("")
	rel, err := e.render(context.Background(), testConfig(), h, "apps", flux.DesiredRelease(h, "apps"),
		testChart(), nil, modeInstall, action.PostRenderStrategyCombined)
	if err != nil {
		t.Fatal(err)
	}

	if out := Assemble(rel, false, false); !strings.Contains(out, "name: demo-hook") {
		t.Errorf("hooks must be included by default:\n%s", out)
	}

	if out := Assemble(rel, true, false); strings.Contains(out, "name: demo-hook") {
		t.Errorf("hooks must be excluded with noHooks:\n%s", out)
	}
	if out := Assemble(nil, false, false); out != "" {
		t.Errorf("a missing release must give an empty manifest, got %q", out)
	}
}

func TestPostRenderStrategyValidation(t *testing.T) {
	tests := []struct {
		flag, spec string
		want       action.PostRenderStrategy
		wantErr    bool
	}{
		{want: action.PostRenderStrategyCombined},
		{spec: "nohooks", want: action.PostRenderStrategyNoHooks},
		{flag: "separate", spec: "nohooks", want: action.PostRenderStrategySeparate},
		{flag: "combind", wantErr: true},
		{spec: "NoHooks", wantErr: true}, // the CRD enum is case-sensitive
	}
	for _, tt := range tests {
		t.Run(tt.flag+"/"+tt.spec, func(t *testing.T) {
			e := &Engine{Opts: Options{PostRenderStrategy: tt.flag}}
			got, err := e.postRenderStrategy(&flux.HelmRelease{Spec: flux.HelmReleaseSpec{PostRenderStrategy: tt.spec}})
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %q, %v; want %q, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestRenderModes(t *testing.T) {
	ch := testChart()
	ch.Templates = append(ch.Templates, &common.File{Name: "templates/mode.yaml", Data: []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: mode
data:
  isUpgrade: "{{ .Release.IsUpgrade }}"
`)})
	tests := []struct {
		mode      renderMode
		isUpgrade bool
	}{
		{mode: modeInstall, isUpgrade: false},
		{mode: modeReplace, isUpgrade: false},
		{mode: modeLockedUpgrade, isUpgrade: true},
	}
	for _, tt := range tests {
		e := &Engine{Opts: Options{DryRun: string(action.DryRunServer), Log: io.Discard}}
		h := testHelmRelease("")
		rel, err := e.render(context.Background(), testConfig(), h, "apps", flux.DesiredRelease(h, "apps"),
			ch, nil, tt.mode, action.PostRenderStrategyCombined)
		if err != nil {
			t.Fatalf("mode %d: %v", tt.mode, err)
		}
		want := fmt.Sprintf(`isUpgrade: "%t"`, tt.isUpgrade)
		if !strings.Contains(rel.Manifest, want) {
			t.Errorf("mode %d: manifest does not contain %s:\n%s", tt.mode, want, rel.Manifest)
		}
	}
}
