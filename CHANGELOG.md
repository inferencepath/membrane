# Changelog

All notable changes to InferencePath are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The error-model and observability contracts are versioned independently (both at
**v1**); a breaking change to either is a MAJOR change and is called out here.

## [Unreleased]

### Changed

- **Console default port is now `8300`** (was `7474`): the built-in default,
  `config/dev.yaml`, `config/membrane.yaml`, the image `EXPOSE`, and the
  `docker-compose.yml` port mapping. To keep the old port, set
  `console.address` explicitly in `membrane.yaml`.
- **Console visual refresh**: updated layout, navigation, tables, and forms. No
  change to the Console's endpoints or editing model.
- The daemon assembly moved from `cmd/membrane` into `internal/daemon`;
  `cmd/membrane` is now a thin wrapper. Behavior is unchanged.

### Added

- **Optional extension hooks**, all inert by default: a content stage and an
  observation stage on the chat path (Interior-only, identity-free, no-op
  defaults), and a generic Console panel host (`GET /api/console/panels`,
  `/api/console/ext/{id}`). With nothing installed, the panel list is empty and
  `/api/console/ext/{id}` returns `503`; request handling is unchanged.
- A `failed_closed` request outcome in metrics/audit, recorded only when an
  installed content stage blocks a request before it crosses the Membrane.
- An architecture test that restricts the core (`cmd/`, `internal/`) to the
  standard library and an explicit allowlist of third-party modules.

### Fixed

- Console: saving a sealed secret now refreshes the Secrets view. The
  `PUT /api/console/secrets/{name}` response reports `"applied": true` (was
  `"set": true`), matching the other Console write endpoints.

## [0.4.0] - 2026-10-13

The first public, source-available **BUSL-1.1** release. It bundles the
managed-configuration model (below) on top of the full Membrane baseline, so
other developers and organizations can pull, build, and run it. As a `0.x`
release the public API may still change before `v1.0.0`.

### Managed configuration (BREAKING)

Membrane now runs a single, **managed** operating model: a small bootstrap file
(`membrane.yaml`) plus an **embedded state store** (pure-Go SQLite under
`data_dir`) that is the single source of truth for all runtime config. The
hand-authored config-file model and the static `bindings` mode are removed. No
cloud and no external database: the store is embedded, the Console and CLI run
locally, and secrets remain by reference (`env:`/`file:`/`vault:`).

#### Added

- **Embedded state store**: a pure-Go SQLite store (`<data_dir>/membrane.db`)
  holding pathways, credential references, Caller Keys (hashes), and the
  permeability policy. The serving snapshot is built from it through the daemon's
  own parsers (identical validation + fail-closed, last-good, atomic reload).
- **Declarative export/apply**: one canonical YAML **bundle** for all kinds, with
  dry-run **diff**, optimistic-concurrency **apply** (single transaction), and a
  `deck`-equivalent CLI: `membrane config export|diff|apply` (Admin-API client).
- **Seed-on-empty**: a fresh store applies a bundle once from `--seed`,
  `$MEMBRANE_SEED`, or `<data_dir>/seed.yaml` (idempotent), the "just works from
  docker compose" bootstrap. Starter at `config/seed.example.yaml`.
- **Console (managed)**: structured editors for every kind plus an **Export /
  Apply** view, all over the managed store; still off the egress path, secrets by
  reference only.
- **Packaging**: `docker-compose.yml` (single service, named volume for
  `data_dir`), Dockerfile `VOLUME` + Go 1.26 builder, a managed `scripts/smoke.sh`.

#### Changed

- `membrane.yaml` carries bootstrap only (`server`, `data_dir`, `audit`,
  `console`). Editing is always available in managed mode (no more "editing
  unavailable" state). `data_dir` is **required**.
- Build/run requires **Go 1.26+**.

#### Removed

- Top-level keys `permeability_path`, `callers_path`, `pathways_path`,
  `keyring_path`, inline `keyring[]`/`bindings[]`, and the `reload`/`pools`/
  `backoff` file blocks (rejected by strict decoding). The standalone sample files
  (`config/{permeability,callers,pathways,credentials,keyring}.yaml`) are
  removed; their content now ships as `config/seed.example.yaml`.
- The static `bindings` mode and the file-based config-as-code mode.

#### Migration

Set `data_dir`, fold the old files into one bundle, and `membrane config apply`
it (or seed it).

### Baseline capabilities

The v0.4.0 baseline delivers the full Membrane model end to end: logical
Pathways, credential custody, provider breadth with resilience, config-as-code
policy with hot reload, and production hardening, with Agent/LLM anonymity
proven on every live path.

#### Added

- **Membrane vertical slice**: `POST /v1/chat` speaking logical Pathways
  (never Provider endpoints): Admit, Authorize, Resolve, Scrub, Transporter
  (+ Egress Identity), Provider, inward Scrub, non-streaming and SSE streaming.
  Caller Key auth (hashed store, constant-time compare) and the Interior-only
  Reveal flag (byte-identical upstream request whether or not Reveal is set).
- **Provider breadth**: OpenAI-compatible (incl. `local`), Anthropic Messages,
  Azure OpenAI, and AWS Bedrock (SigV4) adapters, selected per Pathway from a
  configured pool (`priority` | `round_robin`) with cross-provider streaming
  normalized to canonical chunks.
- **Resilience**: failover across healthy targets, a per-target circuit breaker
  with escalating cooldown, and zero-downtime secret **rotation** (interval +
  `SIGHUP`, last-good on backend outage). Streaming fails over only before the
  first relayed byte.
- **Config-as-code policy**: declarative `pathways.yaml` routing with a
  credentials-only Keyring; per-Caller **deny-by-default authorization**
  (`allow` lists, no enumeration oracle); **hot reload** (`SIGHUP` + interval)
  that is atomic and fail-closed (last-good on invalid candidate).
- **Interior-only usage audit**: one best-effort record per served request
  (`discard` default | `jsonlines`), carrying no content/secret/Reveal/topology
  and never crossing the Membrane.
- **Observability & operability**: identity-free Prometheus metrics (requests,
  latency, tokens, failovers, target health, authz decisions, config reloads),
  `/healthz` · `/readyz` · `/version`, and graceful shutdown.
- **Provider-neutral error model (v1)**: one versioned envelope and code table
  across every surface and gate; no endpoint/credential/region/deployment/ARN
  disclosure; Provider *family* named only under Reveal.
- **Verification depth**: per-family golden request/response fixtures, a
  registry-iterating Diffusion-leak suite (a new family without coverage fails
  CI), and the cross-zone architecture import guards.
- **Release engineering**: multi-stage distroless non-root container image,
  CycloneDX SBOM, `go.sum` verification, `govulncheck`, secret scanning, a
  release smoke test, and operator-provided (never fabricated) image signing.

#### Security

- Secrets are accepted only by reference (`env:` / `file:` / `vault:`); inline
  secrets are rejected by every loader.
- The boundary type system makes an identity leak unrepresentable: a
  `SanitizedRequest` is constructible only by the Scrub, and the Transporter
  accepts nothing else.
- Audit and metrics are identity-free by construction and verified by tests.

[Unreleased]: https://github.com/inferencepath/membrane/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/inferencepath/membrane/releases/tag/v0.4.0
