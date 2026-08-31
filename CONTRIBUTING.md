# Contributing

Keep changes within the public local-agent boundary described in `AGENTS.md`.
Cloud databases, device secrets, signing keys, and private manufacturing policy
do not belong in this repository.

Before opening a pull request, run:

```bash
make check
make test-race
make cross
```

Changes to archive parsing need negative tests for malformed and resource-heavy
inputs. Changes to an executor need cancellation and error-path tests. Hardware
tests must identify the selected station and target clearly and must never run
as an implicit unit-test side effect.

Use focused commits, include the Apache-2.0 SPDX identifier in source files,
and update `api/openapi.yaml` whenever the public HTTP contract changes.
