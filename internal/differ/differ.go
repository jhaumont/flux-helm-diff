// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

// Package differ delegates parsing and rendering to databus23/helm-diff, so the
// output (formats, secret redaction, suppression, rename detection) is the
// one Helm users already know.
package differ

import (
	"io"

	"github.com/databus23/helm-diff/v3/diff"
	"github.com/databus23/helm-diff/v3/manifest"
)

type Options struct {
	OutputFormat       string
	Context            int
	StripTrailingCR    bool
	ShowSecrets        bool
	ShowSecretsDecoded bool
	Normalize          bool
	IncludeTests       bool
	SuppressedKinds    []string
	SuppressRegex      []string
	FindRenames        float32
}

var testHooks = []string{"test", "test-success", "test-failure"}

// Diff writes the differences between two multi-document manifests and
// reports whether there is at least one change.
// Each manifest is parsed with its own default namespace, so a change of
// target namespace shows as objects removed from one and added to the other.
func Diff(oldManifest, oldNamespace, newManifest, newNamespace string, o Options, w io.Writer) bool {
	var excluded []string
	if !o.IncludeTests {
		excluded = testHooks
	}
	oldIdx := manifest.Parse([]byte(oldManifest), oldNamespace, o.Normalize, excluded...)
	newIdx := manifest.Parse([]byte(newManifest), newNamespace, o.Normalize, excluded...)

	return diff.Manifests(oldIdx, newIdx, &diff.Options{
		OutputFormat:              o.OutputFormat,
		OutputContext:             o.Context,
		StripTrailingCR:           o.StripTrailingCR,
		ShowSecrets:               o.ShowSecrets,
		ShowSecretsDecoded:        o.ShowSecretsDecoded,
		SuppressedKinds:           o.SuppressedKinds,
		FindRenames:               o.FindRenames,
		SuppressedOutputLineRegex: o.SuppressRegex,
	}, w)
}
