// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

package manifests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const helmRelease = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: podinfo
spec:
  chart:
    spec:
      chart: podinfo
      sourceRef:
        kind: HelmRepository
        name: podinfo
`

const chartTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
{{- range $k, $v := .Values.data }}
  {{ $k }}: {{ $v }}
{{- end }}
`

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSkipsInvalidFilesInDirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "release.yaml", helmRelease)
	tpl := write(t, dir, "charts/podinfo/templates/configmap.yaml", chartTemplate)

	s, err := Load([]string{dir}, "apps", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.HelmReleases) != 1 || s.HelmReleases[0].Metadata.Namespace != "apps" {
		t.Fatalf("expected the HelmRelease in the apps namespace, got %+v", s.HelmReleases)
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], tpl) {
		t.Fatalf("expected a warning about %s, got %v", tpl, s.Warnings)
	}

	if _, err := Load([]string{tpl}, "apps", strings.NewReader("")); err == nil {
		t.Fatal("an invalid file passed explicitly must be an error")
	}
	if _, err := Load([]string{"-"}, "apps", strings.NewReader(chartTemplate)); err == nil {
		t.Fatal("invalid stdin must be an error")
	}
}
