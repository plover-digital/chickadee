# Security

This is an unvalidated prototype for trusted repositories and reviewed workflows,
not a hardened public runner service. Read the [trust model](docs/architecture.md)
before deployment. No versions currently have a verified real-host release status.

Do not submit secrets, App private keys, JIT configurations, unredacted runner
logs or exploitable vulnerability details to public issues. Use GitHub's [private vulnerability reporting](https://github.com/plover-digital/chickadee/security/advisories/new)
when available. If private reporting is unavailable, request a private contact
without publishing exploit details. There is no commercial vulnerability-response
service or response-time guarantee. Operators should stop affected deployments
while arranging a private report.

Maintainers must document supported versions and security fixes openly, after
coordinated disclosure when necessary. Guest isolation, serial limits, credentials
and restart cleanup require review before any managed-service offering. Private
credentials and vulnerability coordination are exceptions to public development,
not an excuse to hide implementation decisions or verification status.
