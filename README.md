# flux helm-diff

A Flux CLI plugin that shows what a change to a `HelmRelease` manifest would
change on the cluster, before you push it to Git. It is the Flux counterpart of
[databus23/helm-diff](https://github.com/databus23/helm-diff), whose diff engine
and output formats it reuses.

```console
$ flux helm-diff upgrade -f ./apps/podinfo/ -n apps
► HelmRelease apps/podinfo (apps/podinfo/release.yaml)
◎ deployed: apps/podinfo (storage: apps) revision 4, chart podinfo-6.5.3, status deployed
◎ desired:  chart https://stefanprodan.github.io/podinfo/podinfo@6.7.1
apps, podinfo, Deployment (apps) has changed:
  ...
-   replicas: 2
+   replicas: 3
  ...
-         image: ghcr.io/stefanprodan/podinfo:6.5.3
+         image: ghcr.io/stefanprodan/podinfo:6.7.1
```

## How it works

The plugin reproduces, on your workstation, what helm-controller would do:

1. **Release identity.** The release name, target namespace and storage
   namespace are read from the `.status` of the in-cluster HelmRelease (the
   source of truth). If there is none, they are computed with the
   helm-controller rules: `[<targetNamespace>-]<name>`, shortened to 53
   characters with a SHA-256 hash. If the local manifest changes any of the
   three, a warning says the controller will uninstall the release and
   install it again; the diff then shows the old objects removed and the new
   ones added, each in its own target namespace.
2. **Deployed release.** The latest revision is read from the Helm storage
   (`sh.helm.release.v1.*` Secrets), as helm-controller does. This gives the
   manifest and the hooks, already post-rendered by the controller. An
   uninstalled revision counts as no release (a fresh install is shown). A
   pending revision is reported as locked: the controller unlocks it before
   upgrading.
3. **Values.** `.spec.valuesFrom` entries are applied in order, either deep
   merged or set at their `targetPath`, with the same `fluxcd/pkg/chartutil`
   functions as helm-controller (quoted values are set as strings). Optional
   references whose object or key is missing are skipped. `.spec.values` is
   merged on top. ConfigMaps and Secrets found in the
   local files take precedence over the cluster ones, so a values change made
   in the same commit shows up in the diff. SOPS-encrypted Secrets are skipped
   and the cluster version is used instead.
4. **Chart.** The chart is fetched from the Flux source, read from the local
   files first and then from the cluster. Supported sources are an HTTP/S or
   OCI `HelmRepository` (with its `secretRef` credentials and `certSecretRef`
   TLS), an `OCIRepository` and a `HelmChart` referenced by `chartRef`. The
   `OCIRepository` artifact is picked like source-controller does: `digest`,
   then `semver` (with `semverFilter`), then `tag`, then `latest`.
   `valuesFiles` is applied the same way as source-controller does, also with
   `--chart-path`. `--chart-path` and `--chart-version` apply to a single
   HelmRelease only.
5. **Rendering and post-rendering.** The plugin runs
   `helm upgrade --dry-run=server` through the Helm 4 SDK, with the
   `.spec.upgrade` options (`preserveValues`, `crds: Skip`, disabled
   validations, and so on), so `lookup` functions behave as they do in the
   controller. Helm then applies the helm-controller post-renderers following
   `postRenderStrategy`: the Kustomize `postRenderers` (patches, images), then
   `commonMetadata`, then the `helm.toolkit.fluxcd.io/{name,namespace}` origin
   labels, the last two on `metadata` only. Without that last step, every
   resource would show a false label diff. With `--dry-run=client`, templates
   cannot query the cluster (`lookup` returns nothing), but the deployed
   release, the Kubernetes version and the APIs are still read from it.
6. **Diff.** The helm-diff engine compares both sides. YAML normalization is
   on by default: the controller and the plugin can serialize the same
   Kustomize output in a different form.

## Installation

```sh
make install  # test, lint, build, copy to ~/.fluxcd/plugins/flux-helm-diff (or $FLUXCD_PLUGINS)
flux helm-diff --help
```

The plugin then appears under "Plugin Commands" in `flux --help`, and as
`manual` in `flux plugin list`. Once it is published in a catalog, install it
with `flux plugin install helm-diff`.

## Usage

```sh
# A directory (HelmRelease + HelmRepository + values ConfigMaps)
flux helm-diff upgrade -f ./apps/podinfo/

# The full output of a Flux Kustomization (patches, configMapGenerator, ...)
flux build kustomization apps --path ./apps --dry-run | flux helm-diff upgrade -f -

# CI: compact output, exit code 2 when something changes
flux helm-diff upgrade -f ./apps --name podinfo --output simple --detailed-exitcode

# Local chart (under development, or coming from a GitRepository).
# Chart templates found in -f directories are skipped with a warning.
flux helm-diff upgrade -f release.yaml --chart-path ./charts/podinfo

# Differences between two revisions already deployed
flux helm-diff revision podinfo 3 4 -n apps
```

These flags work as they do in helm-diff: `--output`, `--show-secrets`,
`--show-secrets-decoded`, `-q/--suppress-secrets`, `--suppress`,
`--suppress-output-line-regex`, `-D/--find-renames`, `--normalize-manifests`,
`--no-hooks`, `--include-tests`, `--include-crds` and `--detailed-exitcode`.
The one difference is the number of context lines, named `-C/--context-lines`
here, because `--context` is the kubeconfig context, as in the flux CLI.
The global flags are the flux ones: `--context`, `--kubeconfig` and `-n`
(default `flux-system`, used for objects that have no `metadata.namespace`).

## Required permissions

- `get` and `list` on the Helm storage Secrets in the storage namespace.
- `get` on HelmReleases and Flux sources.
- `get` on the ConfigMaps and Secrets used in `valuesFrom`.
- With `--dry-run=server`, read access to the resources queried by the chart's
  `lookup` calls, if any.

## Known limitations

- `GitRepository`, `Bucket` and `ExternalArtifact` sources need
  `--chart-path`: the plugin does not clone repositories.
- OCI credentials for an `OCIRepository` come from `helm registry login` or
  `docker login`, not from its `secretRef`. Cloud `provider` authentication
  (`aws`, `azure`, `gcp`) is not supported either. Both print a warning.
- When helm-controller runs with the `UseHelm3Defaults` feature gate, its
  default `postRenderStrategy` is `nohooks` instead of `combined`: set
  `--post-render-strategy nohooks` to match.
- For a `HelmRelease` with `spec.kubeConfig` (remote cluster), `--context`
  must point to the target cluster, and values are read from that same
  context.
- `serviceAccountName` impersonation is not reproduced: rendering uses your
  own permissions.
- The diff compares against the manifest stored by Helm, not the live state
  of the objects. There is no equivalent of `--three-way-merge` yet; drift is
  already covered by helm-controller's drift detection.
- When a directory holds partial Kustomize patches of the HelmRelease, use
  `flux build kustomization ... | flux helm-diff upgrade -f -`.

## Development

```sh
make test    # tidy, fmt, vet, unit tests
make lint    # golangci-lint
make build   # ./bin/flux-helm-diff
make docker-build  # container image
make help    # all targets
```

Releases are built by goreleaser through the shared
`fluxcd/gha-workflows` CLI plugin workflow when a `v*` tag is pushed. The
archive and checksum names match what the
[fluxcd/plugins](https://github.com/fluxcd/plugins) catalog expects.

## TODO

### End-to-end tests

Add a `make e2e` target, run in CI, that checks the plugin against a real
cluster:

- [ ] `make cluster-up` / `make cluster-down`: create a kind cluster (kind
  downloaded into `bin/`, like golangci-lint) and run `flux install`.
- [ ] Test manifests: a `HelmRelease` from an HTTP `HelmRepository` (with
  `valuesFrom`, `commonMetadata` and a Kustomize post-renderer), and one from
  an `OCIRepository` through `chartRef` with a `targetNamespace`, plus a
  changed copy of both (chart version, values, values ConfigMap).
- [ ] OCIRepository artifact selection, against a local registry
  (`make registry-up` / `make registry-down`, as in flux-mirror) holding
  several podinfo chart versions, including a pre-release such as
  `1.1.0-rc.1`, plus a `latest` tag pointing to an older version (added with
  `crane tag` or `oras tag`). `test/e2e.sh` checks, without
  `--chart-version`, that the chart rendered is the one source-controller
  picks for an `OCIRepository` with:
  - a non-semver `ref.tag`, resolved to its digest;
  - a `ref.digest`;
  - a `ref.semver` range, which picks the highest matching version;
  - a `ref.semver` range with a `ref.semverFilter` that excludes the
    highest match (for example the pre-release);
  - no `ref` at all, which uses the `latest` tag and not the highest
    version.
- [ ] `test/e2e.sh`, which checks that:
  - diffing the deployed manifests exits with 0 (no false diff);
  - diffing the changed manifests exits with 2 with `--detailed-exitcode`
    and shows the expected changes, from files and from stdin (`-f -`);
  - after applying the changed manifests, diffing them again exits with 0;
  - `revision` between revisions 1 and 2 shows the chart upgrade;
  - `flux helm-diff` dispatches to the plugin when the flux CLI supports
    plugins (v2.9 or later).
- [ ] A CI job running these targets, as flux-mirror does.

### Catalog

Publish the plugin in the [fluxcd/plugins](https://github.com/fluxcd/plugins)
catalog with a PR that adds:

- [ ] `plugins/helm-diff.yaml`, the `Plugin` manifest.
- [ ] An entry for `helm-diff` in `catalog.yaml`.
- [ ] `.github/workflows/update-helm-diff.yaml`, calling `update-plugin.yaml`
  with `plugin: helm-diff`, `repo: jhaumont/flux-helm-diff` and
  `bin: flux-helm-diff`.
