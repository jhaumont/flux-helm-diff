# AGENTS.md

Guidance for AI coding assistants working in `jhaumont/flux-helm-diff`. Read this file before making changes.

## Contribution workflow for AI agents

These rules follow [`fluxcd/flux2/CONTRIBUTING.md`](https://github.com/fluxcd/flux2/blob/main/CONTRIBUTING.md), as the Flux repositories do.

- **Do not add `Signed-off-by` or `Co-authored-by` trailers with your agent name.** Only a human can legally certify the DCO.
- **Disclose AI assistance** with an `Assisted-by` trailer naming your agent and model:
  ```sh
  git commit -s -m "Add feature X" --trailer "Assisted-by: <agent-name>/<model-id>"
  ```
  Use this only when explicitly asked to commit. The `-s` flag adds the human's `Signed-off-by` from their git config - do not remove it.
- **Commit message format:** Subject in imperative mood ("Add feature X" instead of "Adding feature X"), capitalized, no trailing period, <=50 characters.
- **Commit body:** Add a succinct explanation of what changed and why, wrap at 72 characters.
- **Trim verbiage:** in PR descriptions, commit messages, and code comments. No marketing prose, no restating the diff, no emojis.
- **Rebase, don't merge:** Never merge `main` into the feature branch; rebase onto the latest `main` and push with `--force-with-lease`. Squash before merge when asked.
- **Tests:** New features, improvements and fixes must have test coverage.

## Project

`flux-helm-diff` is a Flux CLI plugin (`flux helm-diff`) that previews the changes a local `HelmRelease` manifest would make to the Helm release deployed on a cluster. It is the Flux counterpart of [databus23/helm-diff](https://github.com/databus23/helm-diff) and reuses its diff engine and output formats. It is a single Go binary, cobra-based, built on the Helm 4 SDK.

Read the [README](README.md) for an overview of the project and its features.

### Guiding principle

The plugin is only useful if it renders exactly what helm-controller would deploy. Any difference shows up as a false diff, or hides a real one. When changing how releases are identified, how values are composed, how charts are fetched, how the chart is rendered or how it is post-rendered:

- Follow helm-controller and source-controller behavior, not what seems more sensible. Name the controller behavior you reproduce in the code comment.
- Add a test that pins the behavior.
- When the plugin cannot reproduce a behavior, fail with an actionable error or print a `⚠` warning, and list it under "Known limitations" in the README. Never silently approximate.

### Code Structure

- `cmd/flux-helm-diff/` - the `main` package. `main.go` holds the root command, global flags, `main.VERSION` (overridden at build time) and exit-code handling; `upgrade.go` and `revision.go` hold one command each, including the shared `diffFlags` helm-diff options.
- `internal/engine/` - orchestrates one diff: release identity, deployed release, values, chart, dry-run render, assembly of both sides (`Assemble`, shared with `revision`), diff. `ToV1` converts Helm 4 `release.Releaser` values to the v1 release type, the only storage format helm-controller writes.
- `internal/flux/` - minimal, decoupled representations of the Flux custom resources (`HelmRelease`, `HelmRepository`, `OCIRepository`, `HelmChart`) and the helm-controller release naming rules. Only the fields used for rendering are mapped; do not depend on the helm-controller or source-controller Go modules. Do reuse the `github.com/fluxcd/pkg` libraries the controllers call (for example `chartutil` for values), so the behavior is the same by construction.
- `internal/manifests/` - loads local files, directories or stdin and indexes HelmReleases plus the objects they depend on (ConfigMaps, Secrets, Flux sources).
- `internal/kube/` - cluster access: Helm action configurations bound to the storage namespace, and `Lookup`, which resolves objects from local manifests first, then from the cluster.
- `internal/values/` - composes `valuesFrom` and inline values like helm-controller.
- `internal/chartsrc/` - fetches the chart from its Flux source (HTTP/OCI `HelmRepository`, `OCIRepository`, `HelmChart`) or from `--chart-path`, and applies `valuesFiles`.
- `internal/postrender/` - the helm-controller post-renderers (Kustomize patches and images, `commonMetadata`, origin labels), implemented as a Helm 4 `postrenderer.PostRenderer`. `commonMetadata` and origin labels use kustomize transformers on `metadata` only, not `commonLabels`/`commonAnnotations`, which would also touch selectors and pod templates.
- `internal/differ/` - thin adapter over `github.com/databus23/helm-diff/v3`.
- `examples/` - runnable manifests for trying the plugin on a test cluster.

### Build, Test, and Lint

All development goes through the Makefile - do not invoke `go build` directly, because the Makefile stamps `main.VERSION` via `-ldflags` and runs `tidy`/`fmt`/`vet` as prerequisites. The project targets Go 1.27.

- `make build` - build `./bin/flux-helm-diff` with VERSION stamped from git
- `make test` - runs `tidy`, `fmt`, `vet`, then `go test ./... -coverprofile cover.out`
  - Single test pattern: `make test GO_TEST_ARGS="-run TestRenderAppliesPostRenderers"`
- `make lint` - runs golangci-lint with revive, staticcheck, goimports, errcheck, misspell, and related checks
- `make govulncheck` - run Go vulnerability checks
- `make run GO_RUN_ARGS="upgrade -f ./examples/podinfo -n default"` - build then run the CLI with args
- `make install` - test, lint, build, then copy the binary to `~/.fluxcd/plugins` (or `$FLUXCD_PLUGINS`), where the flux CLI discovers it
- `make docker-build` - build the container image (`DOCKER_IMAGE`, default `ghcr.io/jhaumont/flux-helm-diff:latest-dev`)
- `make snapshot` - build the release archives locally with goreleaser

CI (`.github/workflows/test.yaml`) runs `make test`, `make lint`, `make build` and `make docker-build`, then fails if formatting or module files leave the working tree dirty. `cve-scan.yaml` runs govulncheck weekly and on every push. Releases are built by goreleaser through the shared `fluxcd/gha-workflows` CLI plugin workflow when a `v*` tag is pushed; archive and checksum names must keep matching what the [fluxcd/plugins](https://github.com/fluxcd/plugins) catalog expects.

### Code Conventions

- File header: every `.go` file must start with the two-line Apache-2.0 header - enforced by golangci-lint's `revive.file-header` rule.
- Struct tags: only `json` and `inline` are permitted on struct fields (revive `struct-tag` rule).
- Helm SDK: use `helm.sh/helm/v4` only. Release and chart types come from `pkg/release/v1` and `pkg/chart/v2`; convert action results with `engine.ToV1`.
- Post-rendering: pass the post-renderer and `PostRenderStrategy` to the Helm install/upgrade action so Helm applies them as helm-controller does. Never post-render the deployed side: Helm stores the manifest already post-rendered.
- Flags: global flags follow the flux CLI (`--context` is the kubeconfig context, `-n` defaults to `flux-system`); diff flags keep their helm-diff names, except context lines, which are `-C/--context-lines`. Help text is a capitalized sentence ending with a period.
- Cobra commands are built by `new*Cmd()` constructors that keep flag state in a per-command options struct; only `global` is package-level.
- Command output goes through cobra command streams (`cmd.Printf`, `cmd.PrintErrf`, `cmd.OutOrStdout()`, `cmd.ErrOrStderr()`, `cmd.InOrStdin()`). The diff goes to stdout; progress, warnings and errors go to stderr, so the diff can be piped.
- Exit codes: `0` no changes or `--detailed-exitcode` not set, `1` error, `2` changes with `--detailed-exitcode`. Return an `exitError` to set a code; keep this mapping stable, CI jobs depend on it.
- Error handling wraps context with `%w`. Reject invalid input (flags, manifests, unsupported sources) before any network call when possible.
- Local objects passed with `-f` always take precedence over in-cluster ones; a `status` read from Git is never trusted.
- Tests use the standard `testing` package and are table-driven. Rendering tests use an in-memory Helm configuration (memory storage driver, fake kube client) rather than a cluster.

## Writing Documentation

User-facing changes (commands, flags, output, supported sources, required permissions) must be reflected in the docs. The tree is:

- `README.md` - how it works, installation, usage, flag mapping with helm-diff, required permissions, known limitations, development, and the TODO list.
- `examples/` - runnable manifests that should stay aligned with the supported HelmRelease fields.

Apply these rules:

- New or changed CLI flag: update the usage examples and the helm-diff flag mapping in `README.md`.
- New command: add usage examples to `README.md`, and a dedicated reference doc under `docs/` if the command has meaningful flags or output.
- Change to how a release is identified, rendered or post-rendered: update the "How it works" section.
- New supported or unsupported Flux source or HelmRelease field: update "How it works" or "Known limitations".
- New cluster access: update "Required permissions".
- Output or exit-code change: update README examples and the tests that assert them.
