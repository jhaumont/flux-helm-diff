// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

// Package chartsrc downloads the chart a HelmRelease points to, using the
// Flux source objects (local first, then cluster) to find where it lives.
//
// Supported:
//   - .spec.chart with a HelmRepository (HTTP/S or type: oci)
//   - .spec.chartRef to an OCIRepository (digest, semver, tag or latest)
//   - .spec.chartRef to a HelmChart backed by a HelmRepository
//   - any source when --chart-path points to a local chart (GitRepository,
//     Bucket, ExternalArtifact, or a chart under development)
package chartsrc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
	fluxchartutil "github.com/fluxcd/pkg/chartutil"
	"helm.sh/helm/v4/pkg/action"
	ci "helm.sh/helm/v4/pkg/chart"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/registry"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/jhaumont/flux-helm-diff/internal/flux"
)

// Lookup is satisfied by kube.Lookup.
type Lookup interface {
	HelmRepository(ctx context.Context, ns, name string) (*flux.HelmRepository, error)
	OCIRepository(ctx context.Context, ns, name string) (*flux.OCIRepository, error)
	HelmChart(ctx context.Context, ns, name string) (*flux.HelmChart, error)
	Secret(ctx context.Context, ns, name string) (*corev1.Secret, error)
}

type Resolver struct {
	Settings *cli.EnvSettings
	Lookup   Lookup

	// Overrides from the command line.
	ChartPath    string
	ChartVersion string

	InsecureSkipTLSVerify bool
	PlainHTTP             bool

	// Warnf reports source settings the plugin cannot reproduce.
	Warnf func(format string, a ...any)
}

// fetchOptions describe how to reach a chart repository or registry.
type fetchOptions struct {
	username, password string
	passAll            bool
	plainHTTP          bool
	tls                *tlsMaterial
}

// tlsMaterial is the content of a certSecretRef Secret.
type tlsMaterial struct {
	ca, cert, key []byte
}

// Resolved is a loaded chart plus a human description of where it came from.
type Resolved struct {
	Chart  *chart.Chart
	Origin string
}

func (r *Resolver) warnf(format string, a ...any) {
	if r.Warnf != nil {
		r.Warnf(format, a...)
	}
}

func (r *Resolver) Resolve(ctx context.Context, h *flux.HelmRelease, ns string) (*Resolved, error) {
	// The HelmChart template holds the valuesFiles, also with --chart-path.
	var (
		tmpl    *flux.HelmChartTemplateSpec
		tmplNS  = ns
		ociRepo *flux.CrossNamespaceSourceReference
	)
	switch {
	case h.Spec.Chart != nil:
		tmpl = &h.Spec.Chart.Spec
	case h.Spec.ChartRef != nil:
		ref := h.Spec.ChartRef
		refNS := ref.Namespace
		if refNS == "" {
			refNS = ns
		}
		switch ref.Kind {
		case "OCIRepository":
			ociRepo = &flux.CrossNamespaceSourceReference{Kind: ref.Kind, Name: ref.Name, Namespace: refNS}
		case "HelmChart":
			hc, err := r.Lookup.HelmChart(ctx, refNS, ref.Name)
			if err != nil {
				return nil, fmt.Errorf("getting HelmChart %s/%s: %w", refNS, ref.Name, err)
			}
			tmpl, tmplNS = &hc.Spec, refNS
		default:
			if r.ChartPath == "" {
				return nil, fmt.Errorf("chartRef kind %q is not supported, use --chart-path", ref.Kind)
			}
		}
	default:
		return nil, fmt.Errorf("HelmRelease has neither .spec.chart nor .spec.chartRef")
	}

	var (
		res *Resolved
		err error
	)
	switch {
	case r.ChartPath != "":
		var ch *chart.Chart
		if ch, err = loader.Load(r.ChartPath); err != nil {
			return nil, fmt.Errorf("loading chart from %s: %w", r.ChartPath, err)
		}
		res = &Resolved{Chart: ch, Origin: r.ChartPath}
	case ociRepo != nil:
		res, err = r.fromOCIRepository(ctx, ociRepo.Namespace, ociRepo.Name)
	default:
		res, err = r.fromTemplate(ctx, tmplNS, *tmpl)
	}
	if err != nil {
		return nil, err
	}

	if tmpl != nil && len(tmpl.ValuesFiles) > 0 {
		if err := applyValuesFiles(res.Chart, tmpl.ValuesFiles, tmpl.IgnoreMissingValuesFiles); err != nil {
			return nil, err
		}
	}
	if deps := res.Chart.Metadata.Dependencies; len(deps) > 0 {
		reqs := make([]ci.Dependency, 0, len(deps))
		for _, d := range deps {
			reqs = append(reqs, d)
		}
		if err := action.CheckDependencies(res.Chart, reqs); err != nil {
			return nil, fmt.Errorf("chart %s: %w (run `helm dependency build`)", res.Origin, err)
		}
	}
	return res, nil
}

func (r *Resolver) fromTemplate(ctx context.Context, ns string, spec flux.HelmChartTemplateSpec) (*Resolved, error) {
	srcNS := spec.SourceRef.Namespace
	if srcNS == "" {
		srcNS = ns
	}
	version := spec.Version
	if r.ChartVersion != "" {
		version = r.ChartVersion
	}
	if version == "*" {
		version = ""
	}

	switch spec.SourceRef.Kind {
	case "HelmRepository":
		repo, err := r.Lookup.HelmRepository(ctx, srcNS, spec.SourceRef.Name)
		if err != nil {
			return nil, fmt.Errorf("getting HelmRepository %s/%s: %w", srcNS, spec.SourceRef.Name, err)
		}
		opts := fetchOptions{plainHTTP: r.PlainHTTP || repo.Spec.Insecure, passAll: repo.Spec.PassCredentials}
		if ref := repo.Spec.SecretRef; ref != nil {
			sec, err := r.Lookup.Secret(ctx, srcNS, ref.Name)
			if err != nil {
				return nil, fmt.Errorf("reading repository credentials %s/%s: %w", srcNS, ref.Name, err)
			}
			opts.username, opts.password = string(sec.Data["username"]), string(sec.Data["password"])
		}
		if opts.tls, err = r.tlsMaterial(ctx, srcNS, repo.Spec.CertSecretRef); err != nil {
			return nil, err
		}
		if repo.Spec.Type == "oci" || strings.HasPrefix(repo.Spec.URL, "oci://") {
			r.warnProvider("HelmRepository", srcNS, repo.Metadata.Name, repo.Spec.Provider)
			ref := strings.TrimSuffix(repo.Spec.URL, "/") + "/" + spec.Chart
			return r.locate(ref, "", version, opts)
		}
		return r.locate(spec.Chart, repo.Spec.URL, version, opts)

	default:
		return nil, fmt.Errorf("charts from a %s source cannot be fetched by the plugin: "+
			"pass --chart-path pointing to a local copy of %q", spec.SourceRef.Kind, spec.Chart)
	}
}

func (r *Resolver) fromOCIRepository(ctx context.Context, ns, name string) (*Resolved, error) {
	repo, err := r.Lookup.OCIRepository(ctx, ns, name)
	if err != nil {
		return nil, fmt.Errorf("getting OCIRepository %s/%s: %w", ns, name, err)
	}
	r.warnProvider("OCIRepository", ns, name, repo.Spec.Provider)
	if repo.Spec.SecretRef != nil {
		r.warnf("⚠ OCIRepository %s/%s: secretRef is not used, registry credentials come from "+
			"`helm registry login` / `docker login`", ns, name)
	}
	opts := fetchOptions{plainHTTP: r.PlainHTTP || repo.Spec.Insecure}
	if opts.tls, err = r.tlsMaterial(ctx, ns, repo.Spec.CertSecretRef); err != nil {
		return nil, err
	}

	if r.ChartVersion != "" {
		return r.locate(repo.Spec.URL, "", r.ChartVersion, opts)
	}
	ref, version, err := r.ociReference(repo, opts)
	if err != nil {
		return nil, fmt.Errorf("OCIRepository %s/%s: %w", ns, name, err)
	}
	return r.locate(ref, "", version, opts)
}

// ociReference picks the artifact like source-controller
// (OCIRepositoryReconciler.getArtifactRef): digest, then semver (with
// semverFilter), then tag, then "latest". It returns the reference and the
// exact version to pass to Helm. Helm only resolves semver tags, so any
// other tag is pinned to its digest.
func (r *Resolver) ociReference(repo *flux.OCIRepository, opts fetchOptions) (string, string, error) {
	url := repo.Spec.URL
	tag := "latest"
	if ref := repo.Spec.Ref; ref != nil {
		switch {
		case ref.Digest != "":
			return url + "@" + ref.Digest, "", nil
		case ref.SemVer != "":
			v, err := r.tagBySemver(url, ref.SemVer, ref.SemverFilter, opts)
			return url, v, err
		case ref.Tag != "":
			tag = ref.Tag
		}
	}
	if _, err := semver.StrictNewVersion(strings.ReplaceAll(tag, "_", "+")); err == nil {
		return url, tag, nil
	}
	rc, err := r.registryClient(opts)
	if err != nil {
		return "", "", err
	}
	desc, err := rc.Resolve(strings.TrimPrefix(url, registry.OCIScheme+"://") + ":" + tag)
	if err != nil {
		return "", "", fmt.Errorf("resolving tag %q: %w", tag, err)
	}
	return url + "@" + desc.Digest.String(), "", nil
}

// tagBySemver returns the highest tag matching the semver range and the
// semverFilter regex, like source-controller. The filter applies to the OCI
// tag ("_" instead of "+"). Helm charts are pushed with strict semver tags.
func (r *Resolver) tagBySemver(url, rng, filter string, opts fetchOptions) (string, error) {
	constraint, err := semver.NewConstraint(rng)
	if err != nil {
		return "", fmt.Errorf("semver '%s' parse error: %w", rng, err)
	}
	var re *regexp.Regexp
	if filter != "" {
		if re, err = regexp.Compile(filter); err != nil {
			return "", fmt.Errorf("semverFilter '%s': %w", filter, err)
		}
	}
	rc, err := r.registryClient(opts)
	if err != nil {
		return "", err
	}
	tags, err := rc.Tags(strings.TrimPrefix(url, registry.OCIScheme+"://"))
	if err != nil {
		return "", err
	}
	// Tags are sorted from the highest version.
	for _, t := range tags {
		if re != nil && !re.MatchString(strings.ReplaceAll(t, "+", "_")) {
			continue
		}
		if v, err := semver.NewVersion(t); err == nil && constraint.Check(v) {
			return t, nil
		}
	}
	return "", fmt.Errorf("no match found for semver: %s", rng)
}

func (r *Resolver) warnProvider(kind, ns, name, provider string) {
	if provider != "" && provider != "generic" {
		r.warnf("⚠ %s %s/%s: provider %q is not supported, registry credentials come from "+
			"`helm registry login` / `docker login`", kind, ns, name, provider)
	}
}

// tlsMaterial reads a certSecretRef Secret, with the keys source-controller
// accepts (ca.crt, tls.crt, tls.key, and the legacy caFile, certFile, keyFile).
func (r *Resolver) tlsMaterial(ctx context.Context, ns string, ref *flux.SecretRef) (*tlsMaterial, error) {
	if ref == nil {
		return nil, nil
	}
	sec, err := r.Lookup.Secret(ctx, ns, ref.Name)
	if err != nil {
		return nil, fmt.Errorf("reading TLS secret %s/%s: %w", ns, ref.Name, err)
	}
	pick := func(keys ...string) []byte {
		for _, k := range keys {
			if v, ok := sec.Data[k]; ok {
				return v
			}
		}
		return nil
	}
	m := &tlsMaterial{
		ca:   pick("ca.crt", "caFile"),
		cert: pick("tls.crt", "certFile"),
		key:  pick("tls.key", "keyFile"),
	}
	if (m.cert == nil) != (m.key == nil) {
		return nil, fmt.Errorf("TLS secret %s/%s must hold both a certificate and a key", ns, ref.Name)
	}
	return m, nil
}

func (r *Resolver) locate(name, repoURL, version string, opts fetchOptions) (*Resolved, error) {
	rc, err := r.registryClient(opts)
	if err != nil {
		return nil, err
	}
	// In Helm v4 the registry client of ChartPathOptions can only be set
	// through the action embedding it.
	inst := action.NewInstall(action.NewConfiguration())
	inst.SetRegistryClient(rc)
	cpo := &inst.ChartPathOptions
	cpo.RepoURL = repoURL
	cpo.Version = version
	cpo.InsecureSkipTLSVerify = r.InsecureSkipTLSVerify
	cpo.PlainHTTP = opts.plainHTTP
	cpo.Username, cpo.Password, cpo.PassCredentialsAll = opts.username, opts.password, opts.passAll

	if opts.tls != nil {
		// Helm's HTTP getter only takes TLS material from files.
		dir, err := os.MkdirTemp("", "flux-helm-diff-tls-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		write := func(name string, data []byte) (string, error) {
			if data == nil {
				return "", nil
			}
			p := filepath.Join(dir, name)
			return p, os.WriteFile(p, data, 0o600)
		}
		if cpo.CaFile, err = write("ca.crt", opts.tls.ca); err != nil {
			return nil, err
		}
		if cpo.CertFile, err = write("tls.crt", opts.tls.cert); err != nil {
			return nil, err
		}
		if cpo.KeyFile, err = write("tls.key", opts.tls.key); err != nil {
			return nil, err
		}
	}

	path, err := cpo.LocateChart(name, r.Settings)
	if err != nil {
		return nil, fmt.Errorf("locating chart %s (repo %q, version %q): %w", name, repoURL, version, err)
	}
	ch, err := loader.Load(path)
	if err != nil {
		return nil, err
	}
	origin := name
	if repoURL != "" {
		origin = strings.TrimSuffix(repoURL, "/") + "/" + name
	}
	return &Resolved{Chart: ch, Origin: origin + "@" + ch.Metadata.Version}, nil
}

func (r *Resolver) registryClient(opts fetchOptions) (*registry.Client, error) {
	ro := []registry.ClientOption{
		registry.ClientOptWriter(io.Discard),
		registry.ClientOptCredentialsFile(r.Settings.RegistryConfig),
	}
	if opts.plainHTTP {
		ro = append(ro, registry.ClientOptPlainHTTP())
	}
	if opts.username != "" {
		ro = append(ro, registry.ClientOptBasicAuth(opts.username, opts.password))
	}
	if opts.tls != nil || r.InsecureSkipTLSVerify {
		cfg, err := tlsConfig(opts.tls, r.InsecureSkipTLSVerify)
		if err != nil {
			return nil, err
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = cfg
		ro = append(ro, registry.ClientOptHTTPClient(&http.Client{Transport: transport}))
	}
	return registry.NewClient(ro...)
}

func tlsConfig(m *tlsMaterial, insecure bool) (*tls.Config, error) {
	cfg := &tls.Config{InsecureSkipVerify: insecure} //nolint:gosec // --insecure-skip-tls-verify
	if m == nil {
		return cfg, nil
	}
	if m.ca != nil {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(m.ca) {
			return nil, fmt.Errorf("invalid CA certificate in TLS secret")
		}
		cfg.RootCAs = pool
	}
	if m.cert != nil {
		pair, err := tls.X509KeyPair(m.cert, m.key)
		if err != nil {
			return nil, fmt.Errorf("invalid client certificate in TLS secret: %w", err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}

// applyValuesFiles reproduces source-controller (internal/helm/chart
// mergeChartValues + OverwriteChartDefaultValues): the listed files, with
// cleaned paths, are merged in order and replace the chart default values.
// "values.yaml" stands for the chart default values.
func applyValuesFiles(ch *chart.Chart, paths []string, ignoreMissing bool) error {
	merged := map[string]any{}
	for _, p := range paths {
		cfn := filepath.Clean(p)
		if cfn == chartutil.ValuesfileName {
			merged = fluxchartutil.MergeMaps(merged, ch.Values)
			continue
		}
		var data []byte
		for _, f := range ch.Files {
			if f.Name == cfn {
				data = f.Data
				break
			}
		}
		if data == nil {
			if ignoreMissing {
				continue
			}
			return fmt.Errorf("no values file found at path '%s' in chart %s", p, ch.Name())
		}
		v := map[string]any{}
		if err := yaml.Unmarshal(data, &v); err != nil {
			return fmt.Errorf("unmarshaling values from '%s' failed: %w", p, err)
		}
		merged = fluxchartutil.MergeMaps(merged, v)
	}

	for _, f := range ch.Raw {
		if f.Name == chartutil.ValuesfileName {
			ch.Values = merged
			return nil
		}
	}
	return fmt.Errorf("failed to locate values file %s in chart %s", chartutil.ValuesfileName, ch.Name())
}
