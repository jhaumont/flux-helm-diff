// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"helm.sh/helm/v4/pkg/action"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	rcommon "helm.sh/helm/v4/pkg/release/common"
	release "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"

	"github.com/jhaumont/flux-helm-diff/internal/flux"
)

func configMap(value string) string {
	return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\ndata:\n  key: " + value + "\n"
}

func testStorage(t *testing.T, manifests ...string) *action.Configuration {
	t.Helper()
	cfg := action.NewConfiguration()
	cfg.Releases = storage.Init(driver.NewMemory())
	for i, m := range manifests {
		rel := &release.Release{
			Name:      "podinfo",
			Namespace: "apps",
			Version:   i + 1,
			Manifest:  m,
			Info:      &release.Info{Status: rcommon.StatusSuperseded},
			Chart:     &chart.Chart{Metadata: &chart.Metadata{Name: "podinfo", Version: "1.0.0"}},
		}
		if i == len(manifests)-1 {
			rel.Info.Status = rcommon.StatusDeployed
		}
		if err := cfg.Releases.Create(rel); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

func TestDiffRevisions(t *testing.T) {
	cfg := testStorage(t, configMap("one"), configMap("one"), configMap("two"))
	ref := flux.ReleaseRef{Name: "podinfo", TargetNamespace: "apps", StorageNamespace: "apps"}

	tests := []struct {
		name     string
		revs     []string
		detailed bool
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "identical revisions", revs: []string{"1", "2"}, detailed: true},
		{name: "changes without --detailed-exitcode", revs: []string{"1", "3"}, wantOut: "+   key: two"},
		{name: "changes with --detailed-exitcode", revs: []string{"2", "3"}, detailed: true, wantCode: 2, wantOut: "-   key: one"},
		{name: "latest revision by default", revs: []string{"1"}, detailed: true, wantCode: 2, wantOut: "+   key: two"},
		{name: "invalid revision", revs: []string{"one"}, wantErr: `invalid revision "one"`},
		{name: "missing revision", revs: []string{"1", "9"}, wantErr: "getting revision 9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)
			o := &revisionFlags{
				detailedExitCode: tt.detailed,
				diff:             diffFlags{output: "diff", context: -1, normalize: true},
			}

			err := diffRevisions(cmd, cfg, ref, tt.revs, o)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected an error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			code := 0
			var ec exitCoder
			if errors.As(err, &ec) {
				code = ec.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tt.wantCode {
				t.Errorf("exit code %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("output does not contain %q:\n%s", tt.wantOut, out.String())
			}
		})
	}
}
