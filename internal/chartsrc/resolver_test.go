// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

package chartsrc

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/jhaumont/flux-helm-diff/internal/flux"

	"helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"
)

func testChart() *chart.Chart {
	return &chart.Chart{
		Metadata: &chart.Metadata{Name: "demo", Version: "1.0.0"},
		Values:   map[string]any{"replicas": 1, "image": map[string]any{"tag": "v1"}},
		Raw:      []*common.File{{Name: "values.yaml", Data: []byte("replicas: 1\nimage:\n  tag: v1\n")}},
		Files: []*common.File{
			{Name: "values-prod.yaml", Data: []byte("replicas: 3\n")},
			{Name: "ci/values-tag.yaml", Data: []byte("image:\n  tag: v2\n")},
		},
	}
}

func TestApplyValuesFiles(t *testing.T) {
	tests := []struct {
		name          string
		files         []string
		ignoreMissing bool
		want          map[string]any
		wantErr       bool
	}{
		{
			name:  "paths are cleaned like source-controller",
			files: []string{"./values-prod.yaml", "ci//values-tag.yaml"},
			want:  map[string]any{"replicas": float64(3), "image": map[string]any{"tag": "v2"}},
		},
		{
			name:  "values.yaml stands for the chart defaults",
			files: []string{"values.yaml", "values-prod.yaml"},
			want:  map[string]any{"replicas": float64(3), "image": map[string]any{"tag": "v1"}},
		},
		{
			name:    "missing file is an error",
			files:   []string{"values-missing.yaml"},
			wantErr: true,
		},
		{
			name:          "missing file is skipped when ignored",
			files:         []string{"values-missing.yaml", "values-prod.yaml"},
			ignoreMissing: true,
			want:          map[string]any{"replicas": float64(3)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := testChart()
			err := applyValuesFiles(ch, tt.files, tt.ignoreMissing)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(normalize(ch.Values), normalize(tt.want)) {
				t.Fatalf("got %#v\nwant %#v", ch.Values, tt.want)
			}
		})
	}
}

func normalize(v any) any {
	switch m := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range m {
			out[k] = normalize(val)
		}
		return out
	case int:
		return float64(m)
	}
	return v
}

type fakeLookup struct {
	charts  map[string]*flux.HelmChart
	secrets map[string]*corev1.Secret
}

func notFound(resource, name string) error {
	return apierrors.NewNotFound(schema.GroupResource{Resource: resource}, name)
}

func (f fakeLookup) HelmRepository(_ context.Context, _, name string) (*flux.HelmRepository, error) {
	return nil, notFound("helmrepositories", name)
}

func (f fakeLookup) OCIRepository(_ context.Context, _, name string) (*flux.OCIRepository, error) {
	return nil, notFound("ocirepositories", name)
}

func (f fakeLookup) HelmChart(_ context.Context, _, name string) (*flux.HelmChart, error) {
	if c, ok := f.charts[name]; ok {
		return c, nil
	}
	return nil, notFound("helmcharts", name)
}

func (f fakeLookup) Secret(_ context.Context, _, name string) (*corev1.Secret, error) {
	if s, ok := f.secrets[name]; ok {
		return s, nil
	}
	return nil, notFound("secrets", name)
}

func writeChart(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "demo")
	files := map[string]string{
		"Chart.yaml":       "apiVersion: v2\nname: demo\nversion: 1.0.0\n",
		"values.yaml":      "replicas: 1\n",
		"values-prod.yaml": "replicas: 3\n",
	}
	for name, content := range files {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestResolveChartPathKeepsHelmChartValuesFiles(t *testing.T) {
	r := &Resolver{
		ChartPath: writeChart(t),
		Lookup: fakeLookup{charts: map[string]*flux.HelmChart{
			"demo": {Spec: flux.HelmChartTemplateSpec{ValuesFiles: []string{"values.yaml", "values-prod.yaml"}}},
		}},
	}
	h := &flux.HelmRelease{Spec: flux.HelmReleaseSpec{
		ChartRef: &flux.CrossNamespaceSourceReference{Kind: "HelmChart", Name: "demo"},
	}}

	res, err := r.Resolve(context.Background(), h, "apps")
	if err != nil {
		t.Fatal(err)
	}
	if got := normalize(res.Chart.Values); !reflect.DeepEqual(got, map[string]any{"replicas": float64(3)}) {
		t.Fatalf("the HelmChart valuesFiles were not applied: %#v", res.Chart.Values)
	}
}

func TestOCIReferenceDigest(t *testing.T) {
	repo := &flux.OCIRepository{}
	repo.Spec.URL = "oci://ghcr.io/org/charts/demo"
	repo.Spec.Ref = &flux.OCIRepositoryRef{Digest: "sha256:abc", SemVer: "1.x", Tag: "1.0.0"}

	ref, version, err := (&Resolver{}).ociReference(repo, fetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ref != "oci://ghcr.io/org/charts/demo@sha256:abc" || version != "" {
		t.Fatalf("the digest must win over semver and tag, got %q %q", ref, version)
	}

	repo.Spec.Ref.Digest, repo.Spec.Ref.SemVer = "", ""
	if ref, version, err = (&Resolver{}).ociReference(repo, fetchOptions{}); err != nil {
		t.Fatal(err)
	}
	if ref != repo.Spec.URL || version != "1.0.0" {
		t.Fatalf("a semver tag must be passed as the version, got %q %q", ref, version)
	}
}

func TestTLSMaterial(t *testing.T) {
	r := &Resolver{Lookup: fakeLookup{secrets: map[string]*corev1.Secret{
		"tls":    {Data: map[string][]byte{"ca.crt": []byte("CA"), "tls.crt": []byte("C"), "tls.key": []byte("K")}},
		"legacy": {Data: map[string][]byte{"caFile": []byte("CA")}},
		"half":   {Data: map[string][]byte{"tls.crt": []byte("C")}},
	}}}
	ctx := context.Background()

	if m, err := r.tlsMaterial(ctx, "apps", nil); err != nil || m != nil {
		t.Fatalf("no certSecretRef: got %v, %v", m, err)
	}
	m, err := r.tlsMaterial(ctx, "apps", &flux.SecretRef{Name: "tls"})
	if err != nil || string(m.ca) != "CA" || string(m.cert) != "C" || string(m.key) != "K" {
		t.Fatalf("tls keys: got %+v, %v", m, err)
	}
	if m, err = r.tlsMaterial(ctx, "apps", &flux.SecretRef{Name: "legacy"}); err != nil || string(m.ca) != "CA" {
		t.Fatalf("legacy keys: got %+v, %v", m, err)
	}
	if _, err = r.tlsMaterial(ctx, "apps", &flux.SecretRef{Name: "half"}); err == nil {
		t.Fatal("a certificate without a key must be an error")
	}
	if _, err = r.tlsMaterial(ctx, "apps", &flux.SecretRef{Name: "missing"}); err == nil {
		t.Fatal("a missing secret must be an error")
	}
}
