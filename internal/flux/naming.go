// Copyright 2026 The flux-helm-diff Authors
// SPDX-License-Identifier: Apache-2.0

package flux

import (
	"crypto/sha256"
	"fmt"
)

const maxReleaseNameLength = 53

// ReleaseRef identifies a Helm release as handled by helm-controller.
type ReleaseRef struct {
	Name             string
	TargetNamespace  string
	StorageNamespace string
}

func (r ReleaseRef) String() string {
	return fmt.Sprintf("%s/%s (storage: %s)", r.TargetNamespace, r.Name, r.StorageNamespace)
}

// Namespace returns the HelmRelease namespace, defaulting to def.
func (h *HelmRelease) Namespace(def string) string {
	if h.Metadata.Namespace != "" {
		return h.Metadata.Namespace
	}
	return def
}

// DesiredRelease computes the release identity from the spec, using the same
// rules as helm-controller:
//   - name:      .spec.releaseName, else "[<targetNamespace>-]<name>",
//     shortened to 53 chars (40 chars + "-" + 12 chars of SHA-256)
//   - target:    .spec.targetNamespace, else the HelmRelease namespace
//   - storage:   .spec.storageNamespace, else the HelmRelease namespace
func DesiredRelease(h *HelmRelease, defaultNS string) ReleaseRef {
	ns := h.Namespace(defaultNS)

	name := h.Spec.ReleaseName
	if name == "" {
		name = h.Metadata.Name
		if h.Spec.TargetNamespace != "" {
			name = h.Spec.TargetNamespace + "-" + name
		}
	}

	target := h.Spec.TargetNamespace
	if target == "" {
		target = ns
	}
	storage := h.Spec.StorageNamespace
	if storage == "" {
		storage = ns
	}
	return ReleaseRef{Name: ShortenName(name), TargetNamespace: target, StorageNamespace: storage}
}

// ObservedRelease returns the release identity recorded in the status of an
// in-cluster HelmRelease (the source of truth for where the current release
// lives), if any.
func ObservedRelease(h *HelmRelease) (ReleaseRef, bool) {
	if h == nil || len(h.Status.History) == 0 {
		return ReleaseRef{}, false
	}
	latest := h.Status.History[0]
	// Like HelmRelease.GetStorageNamespace in helm-controller: the snapshot
	// namespace is the target namespace, not where the release is stored.
	storage := h.Status.StorageNamespace
	if storage == "" {
		storage = h.Spec.StorageNamespace
	}
	if storage == "" {
		storage = h.Metadata.Namespace
	}
	return ReleaseRef{Name: latest.Name, TargetNamespace: latest.Namespace, StorageNamespace: storage}, true
}

// ShortenName mirrors helm-controller's release name shortening.
func ShortenName(name string) string {
	if len(name) <= maxReleaseNameLength {
		return name
	}
	const hashLen = 12
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
	return name[:maxReleaseNameLength-(hashLen+1)] + "-" + sum[:hashLen]
}
