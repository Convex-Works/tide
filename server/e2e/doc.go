// Package e2e tests klisi's transcripts end to end, the way people run
// them: klisi as main composes it, machines running the real moil binary
// and real uv, and real S3 storage. Its tests skip unless MOIL_BIN names a
// moil binary; see README.md.
package e2e
