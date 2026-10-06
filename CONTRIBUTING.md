# Contributing

Chickadee is developed in the open. Keep design decisions, limitations, test
results and release criteria in this repository. Discuss changes through public
[issues](https://github.com/plover-digital/chickadee/issues) and pull requests. Credentials, host details, runtime
artifacts and vulnerability reports belong in appropriate private channels.

The supported prototype is deliberately narrow: one Linux host, one image profile,
QEMU microvm with KVM, local overlays, a credential-free warm pool, and one job
per runner. Prefer readable Go and simple scripts. Add a lifecycle test for changes
to ownership, credential delivery, cleanup, timeouts or resource accounting.

Before submitting a change:

```sh
make build test
for file in scripts/*.sh guest/*.sh; do bash -n "$file"; done
```

On a restricted environment use `make test-offline` and explicitly report what
could not be verified. A serial simulator is not a real VM boot. Image/network
changes need the relevant real-host acceptance checks in
[validation](docs/validation.md). Never claim job execution or isolation tests that
were not performed. Keep verified dependency checksums and updated provenance with
release changes. Do not attach credentials, JIT values or raw runner logs to issues.

See [roadmap](docs/roadmap.md) for the remaining first-release work and deferred
managed-service direction. MIT licensing permits both self-hosting and commercial
hosting. Contributions are under the repository's MIT license.
