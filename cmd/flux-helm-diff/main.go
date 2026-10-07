// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

// flux-helm-diff is a Flux CLI plugin (`flux helm-diff`) that previews the
// changes a local HelmRelease manifest would make to the Helm release
// currently deployed on a cluster, in the spirit of databus23/helm-diff.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/mgutz/ansi"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// VERSION is overridden at build time by the Makefile and goreleaser.
var VERSION = "0.0.0-dev.0"

type globalFlags struct {
	kubeconfig string
	context    string
	namespace  string
	color      bool
	noColor    bool
	debug      bool
}

var global globalFlags

// exitError carries a specific process exit code (e.g. 2 for --detailed-exitcode).
type exitError struct{ code int }

func (e *exitError) Error() string { return "" }
func (e *exitError) ExitCode() int { return e.code }

// exitCoder lets a command return a non-default exit code.
type exitCoder interface{ ExitCode() int }

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "flux-helm-diff",
		Version: VERSION,
		Short:   "Preview the changes a HelmRelease update would make on the cluster",
		Long: `Flux CLI plugin that renders a local HelmRelease exactly like helm-controller
would (chart from its Flux source, valuesFrom + inline values, post-renderers,
commonMetadata and origin labels) and diffs the result against the manifests
of the Helm release currently deployed on the cluster.

It is the Flux counterpart of the databus23/helm-diff Helm plugin and reuses
its diff engine and output formats.`,
		SilenceUsage:      true,
		SilenceErrors:     true,
		DisableAutoGenTag: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			ansi.DisableColors(!colorEnabled())
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&global.kubeconfig, "kubeconfig", "", "Path to the kubeconfig file.")
	pf.StringVar(&global.context, "context", "", "Kubernetes context to use.")
	pf.StringVarP(&global.namespace, "namespace", "n", "flux-system",
		"Namespace of HelmReleases (and their dependencies) that have no metadata.namespace.")
	pf.BoolVar(&global.color, "color", false, "Force colored output (env FLUX_HELM_DIFF_COLOR).")
	pf.BoolVar(&global.noColor, "no-color", false, "Disable colored output.")
	pf.BoolVar(&global.debug, "debug", false, "Print Helm debug logs.")

	root.AddCommand(newUpgradeCmd(), newRevisionCmd(), newVersionCmd())
	return root
}

func colorEnabled() bool {
	switch {
	case global.noColor:
		return false
	case global.color:
		return true
	}
	if v, ok := os.LookupEnv("FLUX_HELM_DIFF_COLOR"); ok {
		b, _ := strconv.ParseBool(v)
		return b
	}
	return term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("TERM") != "dumb"
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version information",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { cmd.Println(VERSION) },
	}
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	root := newRootCmd()
	err := root.ExecuteContext(ctx)
	if err == nil {
		return
	}
	code := 1
	var ec exitCoder
	if errors.As(err, &ec) {
		code = ec.ExitCode()
	}
	if msg := err.Error(); msg != "" {
		root.PrintErrf("✗ %v\n", msg)
	}
	os.Exit(code)
}
