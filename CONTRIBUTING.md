# Contributing to InferencePath

Thank you for helping build InferencePath. This project protects a single hard
invariant — **Agent/LLM anonymity** — enforced by **credential custody**. Code
quality and production readiness are non-negotiable. Please read this guide
before opening a pull request.

## Ground rules

1. **Never weaken the hard invariant.** No caller or org identity may cross the
   Membrane. If a change makes an identity leak *possible* — even behind a flag
   — it is wrong, regardless of what else it improves.
2. **Secrets are referenced, never inlined.** Provider credentials appear only
   as `env:` / `file:` / `vault:` references. Never commit a secret, in source,
   config, tests, or fixtures.
3. **Audit data never crosses the Membrane.** The Interior audit subsystem
   shares no types with the Exterior.
4. **Fail closed.** An invalid or missing Permeability spec disables the egress
   path (readiness = not-ready). Preserve this behavior.

## Development setup

Requires **Go 1.24+**.

```bash
git clone <your-fork>
cd Membrane
make build
make check   # the full local gate
```

## The quality gate

Before pushing, run:

```bash
make check
```

This runs: build, `go vet`, format check (`gofumpt`), `golangci-lint`, race
tests, coverage thresholds, and `govulncheck`. CI runs the same gate plus a
secret scan and will block merges that fail.

### Coverage thresholds

- **≥ 90%** overall statement coverage.
- **100%** for `internal/membrane/**` and `internal/types/**` (the Membrane and
  the boundary types). Packages that contain only type/interface declarations
  (no executable statements) satisfy this vacuously.

Run `make cover-check` to verify locally.

## Tests are required

Tests are mandatory and are written **first**. In particular:

- New behavior at or near the Membrane must extend the **Diffusion-leak suite**
  (`test/diffusionleak/`) and keep it green.
- Cross-zone import rules are enforced by `test/architecture/`.
- The Permeability loader is exercised against the corpus under
  `test/testdata/permeability/` and the JSON Schema contract.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<optional scope>): <description>

[optional body]
```

Common types: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `ci`,
`build`, `perf`. Example: `feat(membrane): reject duplicate outbound fields`.

The rules are codified in [`.commitlintrc.yaml`](.commitlintrc.yaml). To check
your messages locally:

```bash
npx --yes @commitlint/cli@19 --from=origin/main --config .commitlintrc.yaml
```

## Pull requests

- `main` is protected. Branch from `main`, open a PR, and get a review. **Direct
  pushes to `main` are not permitted**, and history is never force-pushed.
- Keep PRs focused and reviewable.
- Fill in the PR template, including the invariant checklist.
- All CI checks must be green before merge.

## Reporting security issues

Do **not** open a public issue for a vulnerability. Follow
[`SECURITY.md`](SECURITY.md).
