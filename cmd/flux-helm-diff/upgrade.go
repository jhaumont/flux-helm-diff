// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"helm.sh/helm/v4/pkg/cli"

	"github.com/jhaumont/flux-helm-diff/internal/chartsrc"
	"github.com/jhaumont/flux-helm-diff/internal/differ"
	"github.com/jhaumont/flux-helm-diff/internal/engine"
	"github.com/jhaumont/flux-helm-diff/internal/flux"
	"github.com/jhaumont/flux-helm-diff/internal/kube"
	"github.com/jhaumont/flux-helm-diff/internal/manifests"
)

type upgradeFlags struct {
	files              []string
	names              []string
	chartPath          string
	chartVersion       string
	dryRun             string
	helmDriver         string
	noHooks            bool
	includeTests       bool
	includeCRDs        bool
	postRenderStrategy string
	detailedExitCode   bool
	insecure           bool
	plainHTTP          bool
	diff               diffFlags
}

type diffFlags struct {
	output          string
	context         int
	stripTrailingCR bool
	showSecrets     bool
	showDecoded     bool
	suppressSecrets bool
	suppress        []string
	suppressRegex   []string
	findRenames     float32
	normalize       bool
}

func (d *diffFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&d.output, "output", "diff", "Output format: diff, simple, template, json, structured, dyff.")
	f.IntVarP(&d.context, "context-lines", "C", -1, "Number of context lines around changes (-1 = full resources).")
	f.BoolVar(&d.stripTrailingCR, "strip-trailing-cr", false, "Strip trailing carriage returns.")
	f.BoolVar(&d.showSecrets, "show-secrets", false, "Do not redact Secret values.")
	f.BoolVar(&d.showDecoded, "show-secrets-decoded", false, "Show base64-decoded Secret values.")
	f.BoolVarP(&d.suppressSecrets, "suppress-secrets", "q", false, "Suppress Secrets from the output.")
	f.StringArrayVar(&d.suppress, "suppress", nil, "Kinds to suppress from the output (repeatable).")
	f.StringArrayVar(&d.suppressRegex, "suppress-output-line-regex", nil, "Suppress diff lines matching a regex (repeatable).")
	f.Float32VarP(&d.findRenames, "find-renames", "D", 0, "Rename detection threshold (0 = disabled).")
	f.BoolVar(&d.normalize, "normalize-manifests", true,
		"Normalize YAML before diffing so formatting differences are ignored.")
}

func (d *diffFlags) options(includeTests bool) differ.Options {
	kinds := d.suppress
	if d.suppressSecrets {
		kinds = append(kinds, "Secret")
	}
	return differ.Options{
		OutputFormat:       d.output,
		Context:            d.context,
		StripTrailingCR:    d.stripTrailingCR,
		ShowSecrets:        d.showSecrets || d.showDecoded,
		ShowSecretsDecoded: d.showDecoded,
		Normalize:          d.normalize,
		IncludeTests:       includeTests,
		SuppressedKinds:    kinds,
		SuppressRegex:      d.suppressRegex,
		FindRenames:        d.findRenames,
	}
}

func newUpgradeCmd() *cobra.Command {
	o := &upgradeFlags{}
	cmd := &cobra.Command{
		Use:   "upgrade -f FILE [-f FILE...]",
		Short: "Diff local HelmRelease manifests against the releases deployed on the cluster",
		Example: `  # Diff a modified HelmRelease (its HelmRepository / values ConfigMaps can be in the same dir)
  flux helm-diff upgrade -f ./apps/podinfo/

  # Diff the fully built output of a Flux Kustomization (patches, generators applied)
  flux build kustomization apps --path ./apps --dry-run | flux helm-diff upgrade -f -

  # Only one HelmRelease, compact output, fail the CI job when something changes
  flux helm-diff upgrade -f ./apps --name podinfo --output simple --detailed-exitcode

  # Chart under development or coming from a GitRepository
  flux helm-diff upgrade -f ./apps/podinfo/release.yaml --chart-path ./charts/podinfo`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runUpgrade(cmd, o) },
	}

	f := cmd.Flags()
	f.StringArrayVarP(&o.files, "file", "f", nil, "Manifest file or directory, '-' for stdin (repeatable).")
	f.StringSliceVar(&o.names, "name", nil, "Only diff HelmReleases with these names (default: all found).")
	f.StringVar(&o.chartPath, "chart-path", "", "Use this local chart (dir or .tgz) instead of the Flux source.")
	f.StringVar(&o.chartVersion, "chart-version", "", "Override the chart version / semver constraint.")
	f.StringVar(&o.dryRun, "dry-run", "server",
		"'server' renders like helm-controller (lookup works); 'client' renders without querying "+
			"the cluster from templates (lookup returns empty) but still reads the deployed release.")
	f.StringVar(&o.helmDriver, "helm-driver", "secret", "Helm storage driver used by helm-controller.")
	f.BoolVar(&o.noHooks, "no-hooks", false, "Exclude hooks from the diff.")
	f.BoolVar(&o.includeTests, "include-tests", false, "Include Helm test hooks in the diff.")
	f.BoolVar(&o.includeCRDs, "include-crds", false, "Include the chart crds/ directory in the diff.")
	f.StringVar(&o.postRenderStrategy, "post-render-strategy", "",
		"Override .spec.postRenderStrategy (combined, separate, nohooks).")
	f.BoolVar(&o.detailedExitCode, "detailed-exitcode", false, "Exit with code 2 when there are changes.")
	f.BoolVar(&o.insecure, "insecure-skip-tls-verify", false, "Skip TLS verification when downloading charts.")
	f.BoolVar(&o.plainHTTP, "plain-http", false, "Use plain HTTP for OCI registries.")
	o.diff.register(cmd)
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func runUpgrade(cmd *cobra.Command, o *upgradeFlags) error {
	ctx := cmd.Context()
	if o.dryRun != "server" && o.dryRun != "client" {
		return fmt.Errorf("--dry-run must be 'server' or 'client'")
	}
	if o.postRenderStrategy != "" && !engine.ValidPostRenderStrategy(o.postRenderStrategy) {
		return fmt.Errorf("--post-render-strategy must be 'combined', 'separate' or 'nohooks'")
	}

	set, err := manifests.Load(o.files, global.namespace, cmd.InOrStdin())
	if err != nil {
		return err
	}
	for _, w := range set.Warnings {
		cmd.PrintErrln("⚠", w)
	}

	targets, err := selectReleases(set.HelmReleases, o.names)
	if err != nil {
		return err
	}
	if err := checkChartOverrides(o, len(targets)); err != nil {
		return err
	}

	kc, err := kube.New(global.kubeconfig, global.context, global.debug)
	if err != nil {
		return err
	}
	lookup := &kube.Lookup{Local: set, Cluster: kc}
	warnf := func(format string, a ...any) { cmd.PrintErrf(format+"\n", a...) }

	eng := &engine.Engine{
		Kube:   kc,
		Lookup: lookup,
		Resolver: &chartsrc.Resolver{
			Settings:              cli.New(),
			Lookup:                lookup,
			ChartPath:             o.chartPath,
			ChartVersion:          o.chartVersion,
			InsecureSkipTLSVerify: o.insecure,
			PlainHTTP:             o.plainHTTP,
			Warnf:                 warnf,
		},
		Opts: engine.Options{
			DefaultNamespace:   global.namespace,
			HelmDriver:         o.helmDriver,
			DryRun:             o.dryRun,
			NoHooks:            o.noHooks,
			IncludeCRDs:        o.includeCRDs,
			PostRenderStrategy: o.postRenderStrategy,
			Diff:               o.diff.options(o.includeTests),
			Out:                cmd.OutOrStdout(),
			Log:                cmd.ErrOrStderr(),
		},
	}

	anyChange, failures := false, 0
	for _, h := range targets {
		changed, err := eng.Diff(ctx, h)
		if err != nil {
			failures++
			cmd.PrintErrf("✗ %s/%s: %v\n", h.Namespace(global.namespace), h.Metadata.Name, err)
			continue
		}
		anyChange = anyChange || changed
	}

	if failures > 0 {
		return fmt.Errorf("%d HelmRelease(s) could not be diffed", failures)
	}
	if o.detailedExitCode && anyChange {
		return &exitError{code: 2}
	}
	return nil
}

// checkChartOverrides rejects chart overrides that would apply to several
// HelmReleases, each of which points to its own chart.
func checkChartOverrides(o *upgradeFlags, releases int) error {
	if releases <= 1 {
		return nil
	}
	if o.chartPath != "" {
		return fmt.Errorf("--chart-path can only be used with a single HelmRelease (use --name)")
	}
	if o.chartVersion != "" {
		return fmt.Errorf("--chart-version can only be used with a single HelmRelease (use --name)")
	}
	return nil
}

func selectReleases(all []*flux.HelmRelease, names []string) ([]*flux.HelmRelease, error) {
	if len(all) == 0 {
		return nil, fmt.Errorf("no HelmRelease found in the given files")
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}

	seen := map[string]string{}
	found := map[string]bool{}
	var out []*flux.HelmRelease
	for _, h := range all {
		if len(want) > 0 && !want[h.Metadata.Name] {
			continue
		}
		key := h.Metadata.Namespace + "/" + h.Metadata.Name
		if prev, dup := seen[key]; dup {
			return nil, fmt.Errorf("HelmRelease %s is defined twice (%s and %s): "+
				"point -f to a single file or pipe the output of `flux build kustomization`", key, prev, h.Source)
		}
		seen[key] = h.Source
		found[h.Metadata.Name] = true
		out = append(out, h)
	}
	var missing []string
	for _, n := range names {
		if !found[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("no HelmRelease named %s found in the given files", strings.Join(missing, ", "))
	}
	return out, nil
}
