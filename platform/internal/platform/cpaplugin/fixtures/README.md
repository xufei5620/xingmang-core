# CPA plugin assessment fixtures

R214-1a keeps fixtures data-only and repository-owned. Tests synthesize
redacted policy/evidence values at runtime; no native plugin, downloaded
artifact, CPA credential, or prebuilt binary is committed here.

The fixture directory is reserved for deterministic JSON inputs used by the
offline assessor. Any future R214-2 synthetic canary fixture must remain in a
separate approved slice and must be built into a temporary directory, hashed,
and removed after the test.
