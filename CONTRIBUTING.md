# Contributing to InferencePath

InferencePath is source-available under the
[Business Source License 1.1](LICENSE) and is developed by the InferencePath
team. **We are not accepting external pull requests at this time.** Pull
requests opened against this repository may be closed without review.

You are welcome to build, run, and evaluate the code within the terms of the
license, and to tell us what you find.

## Reporting bugs and requesting features

Open a GitHub issue with:

- What you expected and what happened instead.
- Steps to reproduce, including the Membrane version (`/version`) and relevant
  configuration. Use references such as `env:OPENAI_API_KEY`, never real secret
  values.
- Logs or error responses, with any secrets, keys, or internal hostnames
  removed.

## Reporting security issues

Do **not** open a public issue for a vulnerability. Follow
[`SECURITY.md`](SECURITY.md).

## Building from source

Requires **Go 1.26+** (or Docker).

```bash
make build   # build bin/membrane
make test    # race tests with a coverage profile
make check   # the full local quality gate
```

See the [README](README.md) for running Membrane locally and configuring it.

## Commercial licensing

For commercial licensing, contact gaurav@inferencepath.io.
