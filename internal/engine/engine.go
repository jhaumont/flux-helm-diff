// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

// Package engine orchestrates the diff of a local HelmRelease against the
// Helm release deployed on the cluster:
//
//	local HelmRelease ─┬─ release identity (name / target ns / storage ns)
//	                   ├─ values  (valuesFrom + inline, local objects first)
//	                   ├─ chart   (Flux source → Helm repo / OCI registry)
//	                   └─ render  (helm upgrade --dry-run=server, like the controller,
//	                               with its post-renderers: kustomize, commonMetadata,
//	                               origin labels)
//	                                  │
//	deployed release (Helm storage) ──┴──► helm-diff renderer
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	ri "helm.sh/helm/v4/pkg/release"
	rcommon "helm.sh/helm/v4/pkg/release/common"
	release "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage/driver"

	"github.com/jhaumont/flux-helm-diff/internal/chartsrc"
	"github.com/jhaumont/flux-helm-diff/internal/differ"
	"github.com/jhaumont/flux-helm-diff/internal/flux"
	"github.com/jhaumont/flux-helm-diff/internal/kube"
	"github.com/jhaumont/flux-helm-diff/internal/postrender"
	"github.com/jhaumont/flux-helm-diff/internal/values"
)

type Options struct {
	DefaultNamespace   string
	HelmDriver         string
	DryRun             string // "server" (default, like helm-controller) | "client"
	NoHooks            bool
	IncludeCRDs        bool
	PostRenderStrategy string // overrides .spec.postRenderStrategy when set
	Diff               differ.Options

	Out io.Writer // diff output
	Log io.Writer // progress / warnings
}

type Engine struct {
	Kube     *kube.Client
	Lookup   *kube.Lookup
	Resolver *chartsrc.Resolver
	Opts     Options
}

func (e *Engine) logf(format string, a ...any) { _, _ = fmt.Fprintf(e.Opts.Log, format+"\n", a...) }

// Diff renders the desired state of h and diffs it against the deployed
// release. It returns true when changes were detected.
func (e *Engine) Diff(ctx context.Context, h *flux.HelmRelease) (bool, error) {
	ns := h.Namespace(e.Opts.DefaultNamespace)
	e.logf("► HelmRelease %s/%s (%s)", ns, h.Metadata.Name, h.Source)

	strategy, err := e.postRenderStrategy(h)
	if err != nil {
		return false, err
	}

	if h.Spec.KubeConfig != nil {
		e.logf("⚠ spec.kubeConfig is set: the release lives on a remote cluster, " +
			"the current --context must point to it (values are read from the same context)")
	}
	if h.Spec.Suspend {
		e.logf("⚠ HelmRelease is suspended: the change will not be applied until it is resumed")
	}

	// 1. Release identity: where the current release is vs where it will be.
	desired := flux.DesiredRelease(h, e.Opts.DefaultNamespace)
	current := desired
	live, err := e.Kube.HelmRelease(ctx, ns, h.Metadata.Name)
	if err != nil {
		return false, fmt.Errorf("getting in-cluster HelmRelease: %w", err)
	}
	if observed, ok := flux.ObservedRelease(live); ok {
		current = observed
	}
	if current != desired {
		e.logf("⚠ release identity changes: %s → %s; helm-controller will uninstall the old release and install a new one",
			current, desired)
	}

	// 2. Latest stored revision, read like helm-controller
	// (reconcile.DetermineReleaseState).
	curCfg, err := e.Kube.ActionConfig(current, e.Opts.HelmDriver)
	if err != nil {
		return false, err
	}
	locked := false
	curRel, err := ToV1(curCfg.Releases.Last(current.Name))
	switch {
	case errors.Is(err, driver.ErrReleaseNotFound):
		e.logf("◎ no release %s found: showing a fresh install", current)
		curRel = nil
	case err != nil:
		return false, fmt.Errorf("getting release %s: %w", current, err)
	case curRel.Info.Status == rcommon.StatusUninstalled:
		e.logf("◎ release %s revision %d is uninstalled: showing a fresh install", current, curRel.Version)
		curRel = nil
	default:
		e.logf("◎ deployed: %s revision %d, chart %s-%s, status %s",
			current, curRel.Version, curRel.Chart.Metadata.Name, curRel.Chart.Metadata.Version, curRel.Info.Status)
		if curRel.Info.Status.IsPending() {
			locked = true
			e.logf("⚠ release is locked (%s): helm-controller will unlock it before upgrading", curRel.Info.Status)
		}
	}

	// 3. Values.
	vals, err := values.Compose(ctx, e.Lookup, ns, h.Spec.ValuesFrom, h.Spec.Values)
	if err != nil {
		return false, err
	}

	// 4. Chart.
	resolved, err := e.Resolver.Resolve(ctx, h, ns)
	if err != nil {
		return false, err
	}
	e.logf("◎ desired:  chart %s", resolved.Origin)

	// 5. Render with Helm (dry-run).
	newCfg := curCfg
	if current != desired {
		if newCfg, err = e.Kube.ActionConfig(desired, e.Opts.HelmDriver); err != nil {
			return false, err
		}
	}
	mode := modeInstall
	switch {
	case curRel == nil:
	case current != desired:
		// helm-controller uninstalls the old release before installing the
		// new one: its objects must not be reported as ownership conflicts.
		mode = modeReplace
	case locked:
		mode = modeLockedUpgrade
	default:
		mode = modeUpgrade
	}
	newRel, err := e.render(ctx, newCfg, h, ns, desired, resolved.Chart, vals, mode, strategy)
	if err != nil {
		return false, fmt.Errorf("rendering chart: %w", err)
	}

	// 6. Assemble both sides. Both are already post-rendered: the desired one
	// by Helm during the dry-run, the deployed one when it was stored.
	newManifest := Assemble(newRel, e.Opts.NoHooks, e.Opts.IncludeCRDs)
	oldManifest := Assemble(curRel, e.Opts.NoHooks, e.Opts.IncludeCRDs)

	// 7. Diff. Objects without a namespace belong to the target namespace of
	// their own release.
	changed := differ.Diff(oldManifest, current.TargetNamespace, newManifest, desired.TargetNamespace,
		e.Opts.Diff, e.Opts.Out)
	if !changed {
		e.logf("✔ no changes")
	}
	return changed, nil
}

// renderMode is how the desired release is rendered.
type renderMode int

const (
	// modeInstall renders a first install.
	modeInstall renderMode = iota
	// modeUpgrade renders an upgrade of the deployed release.
	modeUpgrade
	// modeLockedUpgrade renders an upgrade of a release in a pending state.
	// Helm refuses to upgrade it, so it is rendered as an install flagged
	// as an upgrade, with the upgrade options.
	modeLockedUpgrade
	// modeReplace renders the install that follows the uninstall of a
	// release whose identity changed.
	modeReplace
)

// postRenderStrategies are the values accepted by helm-controller
// (+kubebuilder:validation:Enum=nohooks;combined;separate).
var postRenderStrategies = map[string]action.PostRenderStrategy{
	"":         action.PostRenderStrategyCombined,
	"combined": action.PostRenderStrategyCombined,
	"separate": action.PostRenderStrategySeparate,
	"nohooks":  action.PostRenderStrategyNoHooks,
}

// ValidPostRenderStrategy reports whether s is a value helm-controller accepts.
func ValidPostRenderStrategy(s string) bool {
	_, ok := postRenderStrategies[s]
	return ok
}

func (e *Engine) postRenderStrategy(h *flux.HelmRelease) (action.PostRenderStrategy, error) {
	s := e.Opts.PostRenderStrategy
	if s == "" {
		s = h.Spec.PostRenderStrategy
	}
	prs, ok := postRenderStrategies[s]
	if !ok {
		return "", fmt.Errorf("invalid postRenderStrategy %q: must be one of combined, separate, nohooks", s)
	}
	return prs, nil
}

func (e *Engine) render(ctx context.Context, cfg *action.Configuration, h *flux.HelmRelease, ns string,
	ref flux.ReleaseRef, ch *chart.Chart, vals map[string]any, mode renderMode,
	prs action.PostRenderStrategy) (*release.Release, error) {

	up := h.Spec.Upgrade
	if up == nil {
		up = &flux.Upgrade{}
	}
	in := h.Spec.Install
	if in == nil {
		in = &flux.Install{}
	}
	dryRun := action.DryRunStrategy(e.Opts.DryRun)
	pr := postrender.New(h, ns)

	if mode == modeUpgrade {
		u := action.NewUpgrade(cfg)
		u.Namespace = ref.TargetNamespace
		u.DryRunStrategy = dryRun
		u.PostRenderer = pr
		u.PostRenderStrategy = prs
		u.ResetValues = !up.PreserveValues
		u.ReuseValues = up.PreserveValues
		u.DisableHooks = up.DisableHooks
		u.DisableOpenAPIValidation = up.DisableOpenAPIValidation
		u.SkipSchemaValidation = up.DisableSchemaValidation
		u.SkipCRDs = strings.EqualFold(up.CRDs, "Skip")
		u.ForceReplace = up.Force
		if h.Spec.MaxHistory != nil {
			u.MaxHistory = *h.Spec.MaxHistory
		}
		rel, err := ToV1(u.RunWithContext(ctx, ref.Name, ch, vals))
		if !errors.Is(err, driver.ErrNoDeployedReleases) {
			return rel, err
		}
		e.logf("⚠ no deployed revision to upgrade from, rendering as an install")
	}

	i := action.NewInstall(cfg)
	i.ReleaseName = ref.Name
	i.Namespace = ref.TargetNamespace
	i.DryRunStrategy = dryRun
	i.PostRenderer = pr
	i.PostRenderStrategy = prs
	i.Replace = true // do not fail because the name is already taken
	i.TakeOwnership = mode == modeReplace
	i.DisableHooks = in.DisableHooks
	i.DisableOpenAPIValidation = in.DisableOpenAPIValidation
	i.SkipSchemaValidation = in.DisableSchemaValidation
	i.SkipCRDs = strings.EqualFold(in.CRDs, "Skip")
	if mode == modeUpgrade || mode == modeLockedUpgrade {
		// .Release.IsUpgrade, no ownership conflicts, upgrade options.
		i.IsUpgrade = true
		i.DisableHooks = up.DisableHooks
		i.DisableOpenAPIValidation = up.DisableOpenAPIValidation
		i.SkipSchemaValidation = up.DisableSchemaValidation
		i.SkipCRDs = strings.EqualFold(up.CRDs, "Skip")
		if mode == modeLockedUpgrade && up.PreserveValues {
			e.logf("⚠ upgrade.preserveValues cannot be reproduced on a locked release: the previous values are not reused")
		}
	}

	if dryRun == action.DryRunClient {
		// A client-side install renders with Helm's default capabilities:
		// use the cluster ones instead, as the upgrade path does.
		if sv, err := e.Kube.Discovery.ServerVersion(); err == nil {
			if kv, err := common.ParseKubeVersion(sv.GitVersion); err == nil {
				i.KubeVersion = kv
			}
		}
		if vs, err := action.GetVersionSet(e.Kube.Discovery); err == nil {
			i.APIVersions = vs
		}
	}
	return ToV1(i.RunWithContext(ctx, ch, vals))
}

// ToV1 converts the release returned by a Helm v4 action to the v1 release
// type, the only storage format helm-controller writes.
func ToV1(rel ri.Releaser, err error) (*release.Release, error) {
	if err != nil {
		return nil, err
	}
	switch r := rel.(type) {
	case *release.Release:
		return r, nil
	case release.Release:
		return &r, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported release type %T", rel)
	}
}

// Assemble builds the comparable manifest of a release: templates, then
// hooks unless noHooks, then the chart CRDs when includeCRDs. A nil release
// gives an empty manifest.
func Assemble(rel *release.Release, noHooks, includeCRDs bool) string {
	if rel == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(rel.Manifest)
	if !noHooks {
		for _, hk := range rel.Hooks {
			fmt.Fprintf(&b, "\n---\n# Source: %s\n%s\n", hk.Path, hk.Manifest)
		}
	}
	if includeCRDs && rel.Chart != nil {
		for _, crd := range rel.Chart.CRDObjects() {
			fmt.Fprintf(&b, "\n---\n# Source: %s\n%s", crd.Filename, crd.File.Data)
		}
	}
	return b.String()
}
