module github.com/Privasys/attested-harness/proxy

go 1.26

require enclave-os-mini/clients/go v0.0.0-00010101000000-000000000000

require github.com/klauspost/compress v1.20.0

// The RA-TLS client SDK rides as a sibling checkout of the repo root
// (attested-harness/ra-tls-clients, gitignored): CI clones it there, local
// dev uses a junction/symlink to the platform checkout. Mirrors the
// confidential-ai build convention.
replace enclave-os-mini/clients/go => ../ra-tls-clients/go
