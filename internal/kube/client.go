// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

// Package kube wraps cluster access: Helm action configurations bound to the
// HelmRelease storage namespace, and lookups of Flux objects / ConfigMaps /
// Secrets with local manifests taking precedence over the cluster.
package kube

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"helm.sh/helm/v4/pkg/action"
	helmkube "helm.sh/helm/v4/pkg/kube"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/jhaumont/flux-helm-diff/internal/flux"
	"github.com/jhaumont/flux-helm-diff/internal/manifests"
)

// Client gives access to the cluster the HelmRelease is reconciled on.
type Client struct {
	debug bool

	// getter is shared by every Helm action configuration: it caches the
	// discovery client and the REST mapper, so diffing many HelmReleases
	// fetches the API discovery documents once.
	getter *genericclioptions.ConfigFlags

	Core      kubernetes.Interface
	Dynamic   dynamic.Interface
	Discovery discovery.DiscoveryInterface
}

func New(kubeconfig, kubeContext string, debug bool) (*Client, error) {
	f := genericclioptions.NewConfigFlags(true)
	if kubeconfig != "" {
		f.KubeConfig = &kubeconfig
	}
	if kubeContext != "" {
		f.Context = &kubeContext
	}
	f.WrapConfigFn = func(rc *rest.Config) *rest.Config {
		rc.QPS, rc.Burst = 50, 100
		return rc
	}
	c := &Client{debug: debug, getter: f}

	rc, err := f.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	if c.Core, err = kubernetes.NewForConfig(rc); err != nil {
		return nil, err
	}
	if c.Dynamic, err = dynamic.NewForConfig(rc); err != nil {
		return nil, err
	}
	if c.Discovery, err = f.ToDiscoveryClient(); err != nil {
		return nil, err
	}
	return c, nil
}

// ActionConfig returns a Helm action configuration whose release storage is
// the HelmRelease storage namespace, while rendered objects default to the
// target namespace (same split as helm-controller). A new configuration is
// returned on every call: Helm actions may replace its storage and client
// (a client-side dry-run does), so configurations are not shared.
func (c *Client) ActionConfig(ref flux.ReleaseRef, driver string) (*action.Configuration, error) {
	out := io.Discard
	if c.debug {
		out = os.Stderr
	}
	cfg := action.NewConfiguration()
	cfg.SetLogger(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := cfg.Init(c.getter, ref.StorageNamespace, driver); err != nil {
		return nil, err
	}
	if kc, ok := cfg.KubeClient.(*helmkube.Client); ok {
		kc.Namespace = ref.TargetNamespace
	}
	return cfg, nil
}

// ---------------------------------------------------------------------------
// Flux objects (served versions are tried in order).

var (
	helmReleaseGVRs = []schema.GroupVersionResource{
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"},
		{Group: "helm.toolkit.fluxcd.io", Version: "v2beta2", Resource: "helmreleases"},
	}
	helmRepositoryGVRs = sourceGVRs("helmrepositories")
	ociRepositoryGVRs  = sourceGVRs("ocirepositories")
	helmChartGVRs      = sourceGVRs("helmcharts")
)

func sourceGVRs(resource string) []schema.GroupVersionResource {
	return []schema.GroupVersionResource{
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: resource},
		{Group: "source.toolkit.fluxcd.io", Version: "v1beta2", Resource: resource},
	}
}

func (c *Client) getTyped(ctx context.Context, gvrs []schema.GroupVersionResource, ns, name string, out any) error {
	var lastErr error
	for _, gvr := range gvrs {
		u, err := c.Dynamic.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, out)
		}
		lastErr = err
		if !apierrors.IsNotFound(err) {
			return err
		}
	}
	return lastErr
}

// HelmRelease returns the in-cluster HelmRelease, or (nil, nil) if absent.
func (c *Client) HelmRelease(ctx context.Context, ns, name string) (*flux.HelmRelease, error) {
	out := &flux.HelmRelease{}
	if err := c.getTyped(ctx, helmReleaseGVRs, ns, name, out); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Layered lookup: local manifests first, then the cluster.

type Lookup struct {
	Local   *manifests.Set
	Cluster *Client
}

func (l *Lookup) ConfigMap(ctx context.Context, ns, name string) (*corev1.ConfigMap, error) {
	if o, ok := l.Local.ConfigMaps[manifests.Key(ns, name)]; ok {
		return o, nil
	}
	return l.Cluster.Core.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
}

func (l *Lookup) Secret(ctx context.Context, ns, name string) (*corev1.Secret, error) {
	if o, ok := l.Local.Secrets[manifests.Key(ns, name)]; ok {
		return o, nil
	}
	return l.Cluster.Core.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
}

func (l *Lookup) HelmRepository(ctx context.Context, ns, name string) (*flux.HelmRepository, error) {
	if o, ok := l.Local.HelmRepositories[manifests.Key(ns, name)]; ok {
		return o, nil
	}
	out := &flux.HelmRepository{}
	return out, l.Cluster.getTyped(ctx, helmRepositoryGVRs, ns, name, out)
}

func (l *Lookup) OCIRepository(ctx context.Context, ns, name string) (*flux.OCIRepository, error) {
	if o, ok := l.Local.OCIRepositories[manifests.Key(ns, name)]; ok {
		return o, nil
	}
	out := &flux.OCIRepository{}
	return out, l.Cluster.getTyped(ctx, ociRepositoryGVRs, ns, name, out)
}

func (l *Lookup) HelmChart(ctx context.Context, ns, name string) (*flux.HelmChart, error) {
	if o, ok := l.Local.HelmCharts[manifests.Key(ns, name)]; ok {
		return o, nil
	}
	out := &flux.HelmChart{}
	return out, l.Cluster.getTyped(ctx, helmChartGVRs, ns, name, out)
}
