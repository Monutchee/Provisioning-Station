# Security policy

Please report vulnerabilities privately through the repository's GitHub
Security Advisory page. Do not open a public issue for an unpatched artifact
validation, command execution, authentication, or provisioning flaw.

Only the latest released version is supported with security fixes during the
project's initial development phase.

The local agent treats Station archives and every HTTP request as untrusted.
Its bearer token protects the API but does not encrypt traffic. Keep the HTTP
listener on loopback unless a token is configured, and use a trusted TLS or
mutually authenticated proxy before allowing remote access. TFTP has no
protocol-level authentication; isolate the provisioning network and provide a
board IP when possible so the agent can restrict the TFTP client.

Release artifact signing, factory authorization policy, cloud credentials,
and secure-boot signing are separate protected systems and are not implemented
by this repository.
