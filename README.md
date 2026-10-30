# InferencePath

> A self-hosted, source-available AI gateway that separates enterprise
> applications from AI providers and **anonymizes inference calls at the egress
> boundary**.

Applications hold only a Membrane **Caller Key** and call logical **Pathways**.
InferencePath attaches pooled provider credentials and strips all caller/org
identity before traffic reaches any provider.

```text
REQUEST LIFECYCLE

+-----------------------+
| Application           |  holds only a Membrane Caller Key + logical Pathway
| (Interior app)        |  (no provider creds, no endpoints)
+-----------------------+
            |
            v  logical chat request (+Caller Key, optional Reveal)

+-------------------------------------------------------------------+
|                            MEMBRANE                               |
|                                                                   |
| Interior:                                                         |
|    Admit      authenticate the Caller Key                         |
|    Authorize  is this Caller allowed this Pathway?                |
|    Resolve    Pathway + operation -> Provider + model             |
|                                                                   |
|               ------- the Membrane boundary -------               |
|                                                                   |
|    SCRUB      Permeability allowlist -> SanitizedRequest only     |
|               drops caller/org identity + control fields          |
|                                                                   |
|    TRANSPORTER + Egress Identity from Keyring (pooled creds)      |
|                 failover + circuit breaker                        |
+-------------------------------------------------------------------+
            |
            v  anonymous upstream request

+-----------------------+
| Provider              |  OpenAI / Anthropic / Azure / Bedrock / local
+-----------------------+
            |
            v  provider response

<--------- inward scrubbed response (provider-neutral by default) ----------+
            |
            +-- Audit remains interior-only (cannot cross Membrane)
```

## The one hard invariant (and the rule that enforces it)

1. **Hard invariant: Agent/LLM anonymity.** No caller or org identity ever
   crosses the **Membrane** to an LLM or agent. Identity is authenticated on the
   **Interior** and destroyed by the **Scrub** before any traffic reaches a
   **Provider**.
2. **Enforcement rule: Credential custody.** Applications never hold provider
   credentials or endpoints. Credentials stay pooled in the **Keyring** at the
   Membrane, forcing every request through the Membrane so the invariant cannot
   be bypassed.

Internal applications are trusted and **may** opt in, per request, via the
Interior-only **Reveal flag** to learn which provider/model answered. The
Reveal flag never crosses the Membrane, so it can only widen what a trusted app
sees on the way back *in*; it can never change what the LLM sees on the way
*out*.

## Status

**v1 capability set.** The full capability set is shipped.
InferencePath delivers logical Pathways with credential custody, provider
breadth with resilience, config-as-code policy with hot reload, and production
hardening, with Agent/LLM anonymity proven on every live path. See
[`CHANGELOG.md`](CHANGELOG.md) for the release history.

**Hardening & release.** Adds an **Interior-only usage audit**
(best-effort, one record per served request; no content/secret/topology, never
crosses the Membrane); **identity-free observability** (`/metrics` with requests,
latency, tokens, failovers, target health, authorization decisions, and config
reloads) plus `/healthz` · `/readyz` · `/version` and graceful shutdown; a
**versioned provider-neutral error model** applied across every surface and gate;
**verification depth** (per-family golden fixtures + a registry-iterating
Diffusion-leak suite); and **release engineering** (distroless non-root image,
CycloneDX SBOM, `govulncheck`, secret scan, smoke test, operator-provided image
signing).

**Policy & config-as-code.** Declarative `pathways.yaml` routing
with a credentials-only Keyring; per-Caller **deny-by-default authorization**
(`allow` lists, generic `403` with no enumeration oracle); and **hot reload**
(`SIGHUP` + interval) that is atomic and fail-closed (last-good on an invalid
candidate; in-flight requests bind to one config snapshot).

**Provider breadth & resilience.** The `chat` Pathway answers
through **any** of several Provider families: **OpenAI-compatible** (incl.
`local`), **Anthropic Messages**, **Azure OpenAI**, and **AWS Bedrock**
(SigV4-signed), selected per Pathway from a configured **pool**, with the
same anonymity guarantees on every path. It adds:

- **Keyring & Pools**: named credential **references** (never inline secrets) and
  per-Pathway pools of ordered targets with a deterministic strategy
  (`priority` | `round_robin`). *(Now the managed store's `credentials` and
  `pathways` kinds; see [configuration](#configuration-managed-mode).)*
- **Rotation**: secret **values** are re-resolved on an interval and on `SIGHUP`
  (zero-downtime); in-flight requests are unaffected and a backend outage keeps
  the last-good value.
- **Failover + circuit breaker**: on a retryable/auth/5xx/timeout failure the
  Membrane fails over to the next healthy target; a per-target breaker skips
  failing targets with an escalating cooldown. Streaming fails over **only before
  the first relayed byte**.
- **Cross-provider streaming**: each family streams incrementally (SSE or the AWS
  event stream) normalized to canonical chunks; the relay is family-agnostic.
- **Provider-neutral error normalization**: every family's failures map to the
  same `502`/`503`/`504` with no endpoint/credential/region/deployment and no
  family identity unless Reveal is set.
- **Identity-free resilience metrics** (failover count, target health) keyed by
  Pathway + a caller-anonymous provider-family label.

**The Membrane core.** The end-to-end proof of
the product: a logical `chat` Pathway flows through **Admit, Resolve, Scrub,
Transporter (+ Egress Identity), an OpenAI-compatible Provider, and the inward Scrub**,
with anonymity proven on the live path. It ships:

- A `POST /v1/chat` endpoint speaking **logical Pathways** (never provider
  endpoints), with **non-streaming** and **SSE streaming** responses.
- **Caller Key** authentication (hashed store, constant-time compare) and a
  static **Binding** resolving `chat` to a pooled Provider + model.
- An **OpenAI-compatible adapter** (OpenAI, vLLM, Ollama, …) whose upstream
  request is built *only* from the `SanitizedRequest` plus the **Egress Identity**
  from the **Keyring**, with no caller/org identity, Caller Key, or Reveal flag.
- The **Reveal flag**: a trusted app may learn `provider`/`model` on the way back
  *in*; the upstream request is byte-identical whether or not Reveal is set.
- **Provider-neutral error handling** (timeout returns `504`, upstream `4xx/5xx` returns `502`)
  that never leaks endpoints, credentials, or (unless Reveal) provider identity.
- The **live Diffusion-leak suite**: a capturing fake Provider asserts the exact
  upstream bytes carry the Egress Identity and *none* of the denylisted
  identity/control fields, across non-streaming, streaming, and error paths.

**Foundations** provide the scaffolding beneath it: the
boundary **types** that make an identity leak *unrepresentable* (`SanitizedRequest`
constructible only by the Scrub), the fail-closed **Permeability** loader with an
outbound denylist, the **Transporter** port that accepts *only* a
`SanitizedRequest`, the operable **server shell** (`/healthz`, `/readyz`,
`/version`, redacting logs, graceful shutdown), the parse-only **config loader**
that rejects inline secrets, and the structural **Diffusion-leak** + cross-zone
**architecture** tests that gate every build.

## Quick start

Requires **Go 1.26+** (or Docker).

```bash
# Build the binary.
make build

# Run the full local quality gate.
make check

# Run in managed mode (bootstrap config + embedded store), seeded so it is READY:
make run     # = membrane --config ./config/dev.yaml --seed ./config/seed.example.yaml
# then, in another shell:
curl -s localhost:8080/healthz
curl -s localhost:8080/readyz
curl -s localhost:8080/version
# and open the Membrane Console at http://127.0.0.1:8300
```

Or the whole stack (store + UI, no external DB) via Docker:

```bash
export OPENAI_API_KEY=sk-... MEMBRANE_CONSOLE_TOKEN=$(openssl rand -hex 24)
docker compose up -d
```

### Using the `chat` Pathway

Applications call **logical Pathways**, never providers. They present only a
**Caller Key** (`<key_id>.<secret>`), never a provider credential or endpoint.

```bash
# The dev profile seeds a `chat` pathway; point it at a local OpenAI-compatible
# server (Ollama/vLLM/…) via the Console or `membrane config apply`. Give the
# credential a placeholder token (local servers ignore it):
export OPENAI_API_KEY=local-dev
./bin/membrane --config ./config/dev.yaml --seed ./config/seed.example.yaml &

# Non-streaming completion (demo Caller Key: ck_demo.s3cr3t-demo-key):
curl -s localhost:8080/v1/chat \
  -H "Authorization: Bearer ck_demo.s3cr3t-demo-key" \
  -H "Content-Type: application/json" \
  -d '{"pathway":"chat","messages":[{"role":"user","content":"Hello"}]}'
# => provider-neutral ChatResponse: no provider/model, no endpoint, no credential.

# Streaming (Server-Sent Events; terminal frame is `data: [DONE]`):
curl -N -s localhost:8080/v1/chat \
  -H "Authorization: Bearer ck_demo.s3cr3t-demo-key" \
  -H "Content-Type: application/json" \
  -d '{"pathway":"chat","stream":true,"messages":[{"role":"user","content":"Count to 3"}]}'

# Opt in to disclosure (Interior-only): the response gains `provider`/`model`,
# but the upstream request is byte-identical to the default call.
curl -s localhost:8080/v1/chat \
  -H "Authorization: Bearer ck_demo.s3cr3t-demo-key" \
  -H "Content-Type: application/json" \
  -d '{"pathway":"chat","reveal":true,"messages":[{"role":"user","content":"hi"}]}'
```

An invalid or missing Caller Key is rejected at Admit with `401` and **no Provider
contact**. The demo key is for local use only; rotate it before any deployment.

### Configuration (managed mode)

Membrane runs a single, **managed** operating model: a small bootstrap file plus
an **embedded state store** (pure-Go SQLite under `data_dir`) that is the single
source of truth for all operational config: routing (pathways), credential
**references**, Caller Keys (hashes), and the Permeability policy. No external
database and no cloud.

- [`config/membrane.yaml`](config/membrane.yaml): bootstrap settings for a
  container/hosted deploy (`server`, `data_dir`, `audit`, `console`).
- [`config/dev.yaml`](config/dev.yaml): local-development bootstrap (loopback
  console, repo-local `data_dir`).
- [`config/seed.example.yaml`](config/seed.example.yaml): a declarative **bundle**
  (all kinds in one document; references/hashes only) applied via seed-on-empty,
  `membrane config apply`, or the Console's Export / Apply.

Configure the store three interchangeable ways: the **Console** (structured
editors or Export / Apply), the **CLI**, or **seed-on-empty**:

```bash
membrane config export -f config.yaml     # snapshot the live config
membrane config diff   -f config.yaml     # dry-run: per-kind change set
membrane config apply  -f config.yaml     # validate, atomic write, hot reload
```

Every edit runs **validate, atomic write, reload** (atomic, fail-closed,
last-good). Secrets are referenced only (`env:` / `file:` / `vault:`); inline
values are rejected. `SIGHUP` forces a reload + secret rotation.

## Developer commands

| Command | Description |
| --- | --- |
| `make build` | Build Membrane binary into `bin/`. |
| `make test` | Run tests with the race detector and a coverage profile. |
| `make cover-check` | Enforce coverage thresholds (90% overall; 100% Membrane + types). |
| `make lint` | Run `golangci-lint` (pinned). |
| `make fmt` / `make fmt-check` | Format / verify formatting. |
| `make vet` | Run `go vet`. |
| `make vuln` | Scan for known vulnerabilities (`govulncheck`). |
| `make secretscan` | Scan for committed secrets (`gitleaks`). |
| `make verify` | Verify the dependency graph against `go.sum`. |
| `make sbom` | Generate a CycloneDX SBOM (`sbom.cdx.json`). |
| `make image` | Build the distroless non-root container image. |
| `make smoke` | Build + start against the sample config and probe operability. |
| `make sign COSIGN_KEY=…` | Sign the image/SBOM (no-op if unset; never fabricated). |
| `make release` | Full release gate: `check` + SBOM + image + signing. |
| `make check` | Run the full local gate. |

## Production

Build and probe a release-style deployment with `make smoke` (builds, starts
against the sample config, and checks `/healthz` · `/readyz` · `/version` and
graceful shutdown). Configure the store through the Console, the `membrane
config` CLI, or seed-on-empty (see [Configuration](#configuration-managed-mode));
every edit is **validate, atomic write, reload** (fail-closed, last-good).
Secrets are referenced only (`env:` / `file:` / `vault:`); inline values are
rejected.

## Quality gate

Every change is guarded by `make check`: build, `go vet`, format check, lint,
race tests, coverage thresholds, and vulnerability scan; `make secretscan` adds a
secret scan. To report a bug or request a feature, see
[`CONTRIBUTING.md`](CONTRIBUTING.md).

## Security

Report vulnerabilities privately per [`SECURITY.md`](SECURITY.md). Never commit
secrets; the config loader and secret scanner are backstops, not substitutes for
care.

## License

Licensed under the [Business Source License 1.1](LICENSE) (`BUSL-1.1`).

Production use is permitted, but the Licensed Work may not be modified and
resold commercially, and organizations with more than twenty (20) employees
must obtain an Enterprise license for production use. On the Change Date (six
years after each version's release) the Licensed Work converts to the Apache
License 2.0. For commercial or Enterprise licensing, contact
gaurav@inferencepath.io.
