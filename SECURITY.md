# Security Policy

## Reporting a vulnerability

Please report suspected security vulnerabilities privately, using
[GitHub security advisories](https://github.com/neilmanfredit/ats-fabric-connector/security/advisories/new)
for this repository. Do not open a public issue for a security report.

Include enough detail to reproduce the issue: affected file(s) or component,
steps to reproduce, and the impact you believe it has. Do not include real
Bullhorn credentials, tokens, tenant identifiers, or record data in a report,
even privately — describe the exposure instead.

## Scope

This covers the code in this repository: the Bullhorn source
(`pkg/source/bullhorn/`), the `fabric/` toolkit, and the CI/CD workflows.
Vulnerabilities in upstream ingestr code should be reported to
[bruin-data/ingestr](https://github.com/bruin-data/ingestr) directly, unless
the issue is specific to how this project uses it.

## Response

This is an alpha, single-maintainer project. There is no guaranteed response
time, but reports are read and acknowledged as soon as possible.
