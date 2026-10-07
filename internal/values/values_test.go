// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

package values

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/jhaumont/flux-helm-diff/internal/flux"
)

type fakeSource struct {
	cms     map[string]*corev1.ConfigMap
	secrets map[string]*corev1.Secret
}

func (f fakeSource) ConfigMap(_ context.Context, _, name string) (*corev1.ConfigMap, error) {
	if cm, ok := f.cms[name]; ok {
		return cm, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, name)
}

func (f fakeSource) Secret(_ context.Context, _, name string) (*corev1.Secret, error) {
	if s, ok := f.secrets[name]; ok {
		return s, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, name)
}

func TestCompose(t *testing.T) {
	src := fakeSource{
		cms: map[string]*corev1.ConfigMap{
			"base": {Data: map[string]string{"values.yaml": "replicaCount: 1\nimage:\n  tag: v1\n  repository: app\n"}},
		},
		secrets: map[string]*corev1.Secret{
			"tls": {Data: map[string][]byte{"crt": []byte("CERT")}},
		},
	}
	refs := []flux.ValuesReference{
		{Kind: "ConfigMap", Name: "base"},
		{Kind: "Secret", Name: "tls", ValuesKey: "crt", TargetPath: "tls.crt"},
		{Kind: "ConfigMap", Name: "missing", Optional: true},
	}
	inline := map[string]any{"image": map[string]any{"tag": "v2"}}

	got, err := Compose(context.Background(), src, "apps", refs, inline)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"replicaCount": float64(1),
		"image":        map[string]any{"tag": "v2", "repository": "app"},
		"tls":          map[string]any{"crt": "CERT"},
	}
	// chartutil.ReadValues decodes numbers as float64.
	if !reflect.DeepEqual(normalize(got), normalize(want)) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestComposeMissingRequired(t *testing.T) {
	_, err := Compose(context.Background(), fakeSource{}, "apps",
		[]flux.ValuesReference{{Kind: "ConfigMap", Name: "nope"}}, nil)
	if err == nil {
		t.Fatal("expected an error for a missing non-optional reference")
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
	case int64:
		return float64(m)
	}
	return v
}

func TestComposeMatchesHelmController(t *testing.T) {
	src := fakeSource{
		cms: map[string]*corev1.ConfigMap{
			"cm": {
				Data: map[string]string{
					"quoted":  `"1234"`,
					"single":  `'true'`,
					"plain":   "1234",
					"literal": "a,b[0]{c}",
				},
				BinaryData: map[string][]byte{"bin": []byte("x: 1")},
			},
		},
	}
	tests := []struct {
		name    string
		ref     flux.ValuesReference
		want    map[string]any
		wantErr bool
	}{
		{
			name: "double-quoted value is set as a string",
			ref:  flux.ValuesReference{Kind: "ConfigMap", Name: "cm", ValuesKey: "quoted", TargetPath: "image.tag"},
			want: map[string]any{"image": map[string]any{"tag": "1234"}},
		},
		{
			name: "single-quoted value is set as a string",
			ref:  flux.ValuesReference{Kind: "ConfigMap", Name: "cm", ValuesKey: "single", TargetPath: "enabled"},
			want: map[string]any{"enabled": "true"},
		},
		{
			name: "unquoted value is typed",
			ref:  flux.ValuesReference{Kind: "ConfigMap", Name: "cm", ValuesKey: "plain", TargetPath: "replicas"},
			want: map[string]any{"replicas": int64(1234)},
		},
		{
			name: "literal value keeps special characters",
			ref:  flux.ValuesReference{Kind: "ConfigMap", Name: "cm", ValuesKey: "literal", TargetPath: "raw", Literal: true},
			want: map[string]any{"raw": "a,b[0]{c}"},
		},
		{
			name: "optional reference with a missing key is skipped",
			ref:  flux.ValuesReference{Kind: "ConfigMap", Name: "cm", ValuesKey: "nope", Optional: true},
			want: map[string]any{},
		},
		{
			name:    "binaryData is not read",
			ref:     flux.ValuesReference{Kind: "ConfigMap", Name: "cm", ValuesKey: "bin"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Compose(context.Background(), src, "apps", []flux.ValuesReference{tt.ref}, nil)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v\nwant %#v", got, tt.want)
			}
		})
	}
}
