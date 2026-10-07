// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

package differ

import (
	"bytes"
	"strings"
	"testing"
)

const configMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: app
data:
  key: value
`

func TestDiffUsesEachSideNamespace(t *testing.T) {
	tests := []struct {
		name         string
		oldNamespace string
		newNamespace string
		changed      bool
		want         []string
	}{
		{
			name:         "same namespace, same content",
			oldNamespace: "apps",
			newNamespace: "apps",
			changed:      false,
		},
		{
			name:         "target namespace moves",
			oldNamespace: "apps",
			newNamespace: "prod",
			changed:      true,
			want:         []string{"apps, app, ConfigMap (v1) to be removed", "prod, app, ConfigMap (v1) to be added"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			changed := Diff(configMap, tt.oldNamespace, configMap, tt.newNamespace,
				Options{OutputFormat: "simple", Context: -1, Normalize: true}, &out)
			if changed != tt.changed {
				t.Fatalf("changed = %v, want %v:\n%s", changed, tt.changed, out.String())
			}
			for _, w := range tt.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output does not contain %q:\n%s", w, out.String())
				}
			}
		})
	}
}
