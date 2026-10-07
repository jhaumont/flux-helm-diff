// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"helm.sh/helm/v4/pkg/action"
	release "helm.sh/helm/v4/pkg/release/v1"

	"github.com/jhaumont/flux-helm-diff/internal/differ"
	"github.com/jhaumont/flux-helm-diff/internal/engine"
	"github.com/jhaumont/flux-helm-diff/internal/flux"
	"github.com/jhaumont/flux-helm-diff/internal/kube"
)

type revisionFlags struct {
	helmDriver       string
	noHooks          bool
	includeTests     bool
	includeCRDs      bool
	detailedExitCode bool
	diff             diffFlags
}

func newRevisionCmd() *cobra.Command {
	o := &revisionFlags{}
	cmd := &cobra.Command{
		Use:   "revision HELMRELEASE REVISION1 [REVISION2]",
		Short: "Diff two revisions of the Helm release managed by an in-cluster HelmRelease",
		Example: `  # What changed between revision 3 and the latest one
  flux helm-diff revision podinfo 3 -n apps

  # What changed between revisions 3 and 4
  flux helm-diff revision podinfo 3 4 -n apps`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error { return runRevision(cmd, o, args) },
	}
	f := cmd.Flags()
	f.StringVar(&o.helmDriver, "helm-driver", "secret", "Helm storage driver used by helm-controller.")
	f.BoolVar(&o.noHooks, "no-hooks", false, "Exclude hooks from the diff.")
	f.BoolVar(&o.includeTests, "include-tests", false, "Include Helm test hooks in the diff.")
	f.BoolVar(&o.includeCRDs, "include-crds", false, "Include the chart crds/ directory in the diff.")
	f.BoolVar(&o.detailedExitCode, "detailed-exitcode", false, "Exit with code 2 when there are changes.")
	o.diff.register(cmd)
	return cmd
}

func runRevision(cmd *cobra.Command, o *revisionFlags, args []string) error {
	ctx := cmd.Context()
	kc, err := kube.New(global.kubeconfig, global.context, global.debug)
	if err != nil {
		return err
	}

	h, err := kc.HelmRelease(ctx, global.namespace, args[0])
	if err != nil {
		return err
	}
	if h == nil {
		return fmt.Errorf("HelmRelease %s/%s not found", global.namespace, args[0])
	}
	ref, ok := flux.ObservedRelease(h)
	if !ok {
		ref = flux.DesiredRelease(h, global.namespace)
	}

	cfg, err := kc.ActionConfig(ref, o.helmDriver)
	if err != nil {
		return err
	}
	return diffRevisions(cmd, cfg, ref, args[1:], o)
}

// diffRevisions diffs two revisions of the release ref: revs[0] and revs[1],
// or the latest revision when revs has a single element.
func diffRevisions(cmd *cobra.Command, cfg *action.Configuration, ref flux.ReleaseRef, revs []string, o *revisionFlags) error {
	r1, err := strconv.Atoi(revs[0])
	if err != nil {
		return fmt.Errorf("invalid revision %q", revs[0])
	}
	old, err := engine.ToV1(cfg.Releases.Get(ref.Name, r1))
	if err != nil {
		return fmt.Errorf("getting revision %d of %s: %w", r1, ref, err)
	}

	var cur *release.Release
	if len(revs) == 2 {
		r2, err := strconv.Atoi(revs[1])
		if err != nil {
			return fmt.Errorf("invalid revision %q", revs[1])
		}
		if cur, err = engine.ToV1(cfg.Releases.Get(ref.Name, r2)); err != nil {
			return fmt.Errorf("getting revision %d of %s: %w", r2, ref, err)
		}
	} else if cur, err = engine.ToV1(cfg.Releases.Last(ref.Name)); err != nil {
		return fmt.Errorf("getting the latest revision of %s: %w", ref, err)
	}

	cmd.PrintErrf("► %s: revision %d (%s) → %d (%s)\n",
		ref, old.Version, old.Chart.Metadata.Version, cur.Version, cur.Chart.Metadata.Version)

	changed := differ.Diff(
		engine.Assemble(old, o.noHooks, o.includeCRDs), old.Namespace,
		engine.Assemble(cur, o.noHooks, o.includeCRDs), cur.Namespace,
		o.diff.options(o.includeTests), cmd.OutOrStdout())
	if !changed {
		cmd.PrintErrln("✔ no changes")
	}
	if o.detailedExitCode && changed {
		return &exitError{code: 2}
	}
	return nil
}
