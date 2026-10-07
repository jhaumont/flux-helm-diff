// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

// Package values composes the final Helm values of a HelmRelease by calling
// the function helm-controller uses, fluxcd/pkg/chartutil
// ChartValuesFromReferences, over the plugin's lookup (local manifests
// first, then the cluster). The behavior is the controller's by construction.
package values

import (
	"context"
	"fmt"

	"github.com/fluxcd/pkg/apis/meta"
	fluxchartutil "github.com/fluxcd/pkg/chartutil"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jhaumont/flux-helm-diff/internal/flux"
)

// Source resolves the ConfigMaps / Secrets referenced in valuesFrom. A
// missing object must be reported with a Kubernetes NotFound error.
type Source interface {
	ConfigMap(ctx context.Context, ns, name string) (*corev1.ConfigMap, error)
	Secret(ctx context.Context, ns, name string) (*corev1.Secret, error)
}

// Compose returns the values helm-controller would pass to Helm.
func Compose(ctx context.Context, src Source, ns string, refs []flux.ValuesReference, inline map[string]any) (map[string]any, error) {
	metaRefs := make([]meta.ValuesReference, 0, len(refs))
	for _, r := range refs {
		metaRefs = append(metaRefs, meta.ValuesReference{
			Kind:       r.Kind,
			Name:       r.Name,
			ValuesKey:  r.ValuesKey,
			TargetPath: r.TargetPath,
			Optional:   r.Optional,
			Literal:    r.Literal,
		})
	}
	vals, err := fluxchartutil.ChartValuesFromReferences(ctx, logr.Discard(), reader{src: src}, ns, inline, metaRefs...)
	if err != nil {
		return nil, err
	}
	return vals, nil
}

// reader adapts a Source to the controller-runtime client that
// ChartValuesFromReferences expects. It only ever calls Get; the embedded
// nil interface makes any other call panic instead of silently doing nothing.
type reader struct {
	kubeclient.Client
	src Source
}

func (r reader) Get(ctx context.Context, key kubeclient.ObjectKey, obj kubeclient.Object, _ ...kubeclient.GetOption) error {
	switch o := obj.(type) {
	case *corev1.ConfigMap:
		cm, err := r.src.ConfigMap(ctx, key.Namespace, key.Name)
		if err != nil {
			return err
		}
		cm.DeepCopyInto(o)
	case *corev1.Secret:
		sec, err := r.src.Secret(ctx, key.Namespace, key.Name)
		if err != nil {
			return err
		}
		sec.DeepCopyInto(o)
	default:
		return fmt.Errorf("valuesFrom: unsupported object %T", obj)
	}
	return nil
}
