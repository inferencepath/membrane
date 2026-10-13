# Security Policy

InferencePath exists to uphold one hard invariant — **Agent/LLM anonymity** —
enforced by **credential custody**. Security reports are taken seriously.

## Reporting a vulnerability

**Please do not open a public issue for security vulnerabilities.**

Instead, report privately using GitHub's
[private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability)
on this repository ("Security" tab → "Report a vulnerability").

Please include:

- A description of the issue and its impact.
- Steps to reproduce (a minimal proof of concept if possible).
- Affected versions/commit, and any suggested remediation.

We will acknowledge your report, investigate, and keep you informed of
remediation progress. Please allow a reasonable time for a fix before any
public disclosure, and coordinate disclosure with us.

## What we consider high severity

Because of what this project is, the following are treated as top priority:

- **Diffusion leak** — any path by which caller or org identity (or an
  Interior-only control field such as the Reveal flag) can cross the Membrane to
  a Provider/LLM/agent.
- **Credential custody bypass** — any way an application can obtain, infer, or
  directly use provider credentials or endpoints.
- **Fail-open behavior** — any case where an invalid/missing Permeability spec
  still enables the egress path.
- **Secret exposure** — secrets appearing in logs, audit records, error
  messages, config, or on the egress wire.

## Supported versions

This project is in the `0.x` series. Security fixes are applied to `main` and
released in the latest `0.x` tag (currently `v0.4.0`); there is no long-term
support for older `0.x` tags. Always run the most recent release.

## Secret handling

Secrets are referenced only (`env:` / `file:` / `vault:`); inline secrets are
rejected by the config loader. Logs redact sensitive attributes. A secret
scanner (`gitleaks`) runs in CI. These are backstops — never commit a secret.
