// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/jhaumont/flux-helm-diff/internal/flux"
)

func TestSelectReleases(t *testing.T) {
	hr := func(ns, name, src string) *flux.HelmRelease {
		return &flux.HelmRelease{Metadata: flux.ObjectMeta{Name: name, Namespace: ns}, Source: src}
	}
	all := []*flux.HelmRelease{hr("apps", "podinfo", "a.yaml"), hr("apps", "redis", "b.yaml")}

	tests := []struct {
		name    string
		all     []*flux.HelmRelease
		names   []string
		want    int
		wantErr string
	}{
		{name: "all releases by default", all: all, want: 2},
		{name: "filter by name", all: all, names: []string{"redis"}, want: 1},
		{name: "every requested name must exist", all: all, names: []string{"podinfo", "redsi"}, wantErr: "redsi"},
		{name: "no release at all", wantErr: "no HelmRelease found"},
		{
			name:    "duplicate release",
			all:     []*flux.HelmRelease{hr("apps", "podinfo", "a.yaml"), hr("apps", "podinfo", "b.yaml")},
			wantErr: "defined twice",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectReleases(tt.all, tt.names)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected an error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tt.want {
				t.Fatalf("got %d releases, want %d", len(got), tt.want)
			}
		})
	}
}

func TestCheckChartOverrides(t *testing.T) {
	tests := []struct {
		name     string
		o        upgradeFlags
		releases int
		wantErr  string
	}{
		{name: "no override", releases: 3},
		{name: "chart path with one release", o: upgradeFlags{chartPath: "./chart"}, releases: 1},
		{name: "chart version with one release", o: upgradeFlags{chartVersion: "1.2.3"}, releases: 1},
		{name: "chart path with several releases", o: upgradeFlags{chartPath: "./chart"}, releases: 2, wantErr: "--chart-path"},
		{name: "chart version with several releases", o: upgradeFlags{chartVersion: "1.2.3"}, releases: 2, wantErr: "--chart-version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkChartOverrides(&tt.o, tt.releases)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected an error about %s, got %v", tt.wantErr, err)
			}
		})
	}
}
