// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

package flux

import (
	"strings"
	"testing"
)

func TestDesiredRelease(t *testing.T) {
	tests := []struct {
		name string
		hr   HelmRelease
		want ReleaseRef
	}{
		{
			name: "defaults to HelmRelease name and namespace",
			hr:   HelmRelease{Metadata: ObjectMeta{Name: "podinfo", Namespace: "apps"}},
			want: ReleaseRef{Name: "podinfo", TargetNamespace: "apps", StorageNamespace: "apps"},
		},
		{
			name: "target namespace prefixes the release name",
			hr: HelmRelease{
				Metadata: ObjectMeta{Name: "podinfo", Namespace: "flux-system"},
				Spec:     HelmReleaseSpec{TargetNamespace: "prod"},
			},
			want: ReleaseRef{Name: "prod-podinfo", TargetNamespace: "prod", StorageNamespace: "flux-system"},
		},
		{
			name: "explicit release name and storage namespace win",
			hr: HelmRelease{
				Metadata: ObjectMeta{Name: "podinfo", Namespace: "flux-system"},
				Spec:     HelmReleaseSpec{ReleaseName: "web", TargetNamespace: "prod", StorageNamespace: "prod"},
			},
			want: ReleaseRef{Name: "web", TargetNamespace: "prod", StorageNamespace: "prod"},
		},
		{
			name: "missing metadata.namespace falls back to the default namespace",
			hr:   HelmRelease{Metadata: ObjectMeta{Name: "podinfo"}},
			want: ReleaseRef{Name: "podinfo", TargetNamespace: "flux-system", StorageNamespace: "flux-system"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DesiredRelease(&tt.hr, "flux-system"); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestShortenName(t *testing.T) {
	long := "a-very-lengthy-target-namespace-with-a-nice-object-name"
	got := ShortenName(long)
	if len(got) != 53 {
		t.Fatalf("expected 53 chars, got %d (%s)", len(got), got)
	}
	if !strings.HasPrefix(got, long[:40]+"-") {
		t.Fatalf("unexpected prefix: %s", got)
	}
	if ShortenName("short") != "short" {
		t.Fatal("short names must be left untouched")
	}
}

func TestObservedRelease(t *testing.T) {
	tests := []struct {
		name string
		hr   *HelmRelease
		want ReleaseRef
		ok   bool
	}{
		{name: "no HelmRelease", hr: nil},
		{name: "no history", hr: &HelmRelease{Metadata: ObjectMeta{Name: "podinfo", Namespace: "apps"}}},
		{
			name: "storage namespace from the status",
			hr: &HelmRelease{
				Metadata: ObjectMeta{Name: "podinfo", Namespace: "flux-system"},
				Status: HelmReleaseStatus{
					StorageNamespace: "storage",
					History:          []Snapshot{{Name: "apps-podinfo", Namespace: "apps"}},
				},
			},
			want: ReleaseRef{Name: "apps-podinfo", TargetNamespace: "apps", StorageNamespace: "storage"},
			ok:   true,
		},
		{
			name: "storage namespace defaults to the HelmRelease namespace, not the target one",
			hr: &HelmRelease{
				Metadata: ObjectMeta{Name: "podinfo", Namespace: "flux-system"},
				Spec:     HelmReleaseSpec{TargetNamespace: "apps"},
				Status:   HelmReleaseStatus{History: []Snapshot{{Name: "apps-podinfo", Namespace: "apps"}}},
			},
			want: ReleaseRef{Name: "apps-podinfo", TargetNamespace: "apps", StorageNamespace: "flux-system"},
			ok:   true,
		},
		{
			name: "storage namespace from the spec when the status has none",
			hr: &HelmRelease{
				Metadata: ObjectMeta{Name: "podinfo", Namespace: "flux-system"},
				Spec:     HelmReleaseSpec{TargetNamespace: "apps", StorageNamespace: "storage"},
				Status:   HelmReleaseStatus{History: []Snapshot{{Name: "apps-podinfo", Namespace: "apps"}}},
			},
			want: ReleaseRef{Name: "apps-podinfo", TargetNamespace: "apps", StorageNamespace: "storage"},
			ok:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ObservedRelease(tt.hr)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("got %+v, %v; want %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
