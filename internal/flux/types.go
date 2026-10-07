// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

// Package flux holds minimal, decoupled representations of the Flux custom
// resources this plugin needs. Only the fields used for rendering are mapped,
// which keeps the plugin independent from helm-controller / source-controller
// Go modules (and their frequent dependency bumps). JSON tags follow the CRD
// schemas of helm.toolkit.fluxcd.io/v2 and source.toolkit.fluxcd.io/v1.
package flux

// ObjectMeta is the subset of metav1.ObjectMeta we care about.
type ObjectMeta struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// HelmRelease mirrors helm.toolkit.fluxcd.io/v2 HelmRelease.
type HelmRelease struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   ObjectMeta        `json:"metadata"`
	Spec       HelmReleaseSpec   `json:"spec"`
	Status     HelmReleaseStatus `json:"status,omitempty"`

	// Source is the file the object was read from (empty for cluster objects).
	Source string `json:"-"`
}

type HelmReleaseSpec struct {
	Chart              *HelmChartTemplate             `json:"chart,omitempty"`
	ChartRef           *CrossNamespaceSourceReference `json:"chartRef,omitempty"`
	ReleaseName        string                         `json:"releaseName,omitempty"`
	TargetNamespace    string                         `json:"targetNamespace,omitempty"`
	StorageNamespace   string                         `json:"storageNamespace,omitempty"`
	KubeConfig         map[string]any                 `json:"kubeConfig,omitempty"`
	MaxHistory         *int                           `json:"maxHistory,omitempty"`
	Install            *Install                       `json:"install,omitempty"`
	Upgrade            *Upgrade                       `json:"upgrade,omitempty"`
	ValuesFrom         []ValuesReference              `json:"valuesFrom,omitempty"`
	Values             map[string]any                 `json:"values,omitempty"`
	PostRenderers      []PostRenderer                 `json:"postRenderers,omitempty"`
	PostRenderStrategy string                         `json:"postRenderStrategy,omitempty"`
	CommonMetadata     *CommonMetadata                `json:"commonMetadata,omitempty"`
	Suspend            bool                           `json:"suspend,omitempty"`
}

type HelmChartTemplate struct {
	Spec HelmChartTemplateSpec `json:"spec"`
}

type HelmChartTemplateSpec struct {
	Chart                    string                        `json:"chart"`
	Version                  string                        `json:"version,omitempty"`
	SourceRef                CrossNamespaceObjectReference `json:"sourceRef"`
	ValuesFiles              []string                      `json:"valuesFiles,omitempty"`
	IgnoreMissingValuesFiles bool                          `json:"ignoreMissingValuesFiles,omitempty"`
}

type CrossNamespaceObjectReference struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
}

type CrossNamespaceSourceReference = CrossNamespaceObjectReference

type ValuesReference struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	ValuesKey  string `json:"valuesKey,omitempty"`
	TargetPath string `json:"targetPath,omitempty"`
	Optional   bool   `json:"optional,omitempty"`
	Literal    bool   `json:"literal,omitempty"`
}

type Install struct {
	CRDs                     string `json:"crds,omitempty"`
	DisableHooks             bool   `json:"disableHooks,omitempty"`
	DisableOpenAPIValidation bool   `json:"disableOpenAPIValidation,omitempty"`
	DisableSchemaValidation  bool   `json:"disableSchemaValidation,omitempty"`
}

type Upgrade struct {
	CRDs                     string `json:"crds,omitempty"`
	DisableHooks             bool   `json:"disableHooks,omitempty"`
	DisableOpenAPIValidation bool   `json:"disableOpenAPIValidation,omitempty"`
	DisableSchemaValidation  bool   `json:"disableSchemaValidation,omitempty"`
	PreserveValues           bool   `json:"preserveValues,omitempty"`
	Force                    bool   `json:"force,omitempty"`
}

type PostRenderer struct {
	Kustomize *Kustomize `json:"kustomize,omitempty"`
}

type Kustomize struct {
	Patches []Patch `json:"patches,omitempty"`
	Images  []Image `json:"images,omitempty"`
}

type Patch struct {
	Patch  string    `json:"patch,omitempty"`
	Target *Selector `json:"target,omitempty"`
}

type Selector struct {
	Group              string `json:"group,omitempty"`
	Version            string `json:"version,omitempty"`
	Kind               string `json:"kind,omitempty"`
	Name               string `json:"name,omitempty"`
	Namespace          string `json:"namespace,omitempty"`
	AnnotationSelector string `json:"annotationSelector,omitempty"`
	LabelSelector      string `json:"labelSelector,omitempty"`
}

type Image struct {
	Name    string `json:"name"`
	NewName string `json:"newName,omitempty"`
	NewTag  string `json:"newTag,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

type CommonMetadata struct {
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type HelmReleaseStatus struct {
	StorageNamespace string     `json:"storageNamespace,omitempty"`
	History          []Snapshot `json:"history,omitempty"`
}

// Snapshot is an entry of .status.history (most recent first).
type Snapshot struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Version      int    `json:"version"`
	ChartName    string `json:"chartName,omitempty"`
	ChartVersion string `json:"chartVersion,omitempty"`
	Status       string `json:"status,omitempty"`
}

// ---------------------------------------------------------------------------
// source.toolkit.fluxcd.io

type SecretRef struct {
	Name string `json:"name"`
}

type HelmRepository struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		URL             string     `json:"url"`
		Type            string     `json:"type,omitempty"` // "default" | "oci"
		Provider        string     `json:"provider,omitempty"`
		SecretRef       *SecretRef `json:"secretRef,omitempty"`
		CertSecretRef   *SecretRef `json:"certSecretRef,omitempty"`
		PassCredentials bool       `json:"passCredentials,omitempty"`
		Insecure        bool       `json:"insecure,omitempty"`
	} `json:"spec"`
}

type OCIRepository struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		URL           string            `json:"url"`
		Insecure      bool              `json:"insecure,omitempty"`
		Provider      string            `json:"provider,omitempty"`
		SecretRef     *SecretRef        `json:"secretRef,omitempty"`
		CertSecretRef *SecretRef        `json:"certSecretRef,omitempty"`
		Ref           *OCIRepositoryRef `json:"ref,omitempty"`
	} `json:"spec"`
}

// OCIRepositoryRef selects the artifact of an OCIRepository.
type OCIRepositoryRef struct {
	Tag          string `json:"tag,omitempty"`
	SemVer       string `json:"semver,omitempty"`
	SemverFilter string `json:"semverFilter,omitempty"`
	Digest       string `json:"digest,omitempty"`
}

type HelmChart struct {
	Metadata ObjectMeta            `json:"metadata"`
	Spec     HelmChartTemplateSpec `json:"spec"`
}
