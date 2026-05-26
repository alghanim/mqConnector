// Package cluster fingerprints DLQ error strings so that semantically
// similar errors collapse to the same opaque key.
//
// # Why
//
// The DLQ Intelligence Console rolls thousands of individual DLQ rows
// up into a handful of error clusters sorted by impact. The naive
// approach — GROUP BY error_reason — fragments badly the moment a real
// error includes a timestamp, a UUID, a customer id, or a port
// number. Two messages failing the same validation against different
// payloads should land in the same cluster; two messages failing
// different validations against the same payload should not.
//
// The strategy
//
//  1. Tokenise the error text. Lowercase, collapse whitespace, then
//     run a fixed list of regex substitutions that replace concrete
//     variable parts with abstract placeholders: <UUID>, <TIME>,
//     <INT>, <EMAIL>, <HOST>, <PATH>, <FIELD>, <STR>. The result is
//     the human-readable "template".
//  2. Hash the tokenised template with SimHash over 64 bits using
//     FNV-1a as the underlying token hash. SimHash has the property
//     that templates that share most tokens produce hashes that
//     differ in few bits — and since two messages drawn from the same
//     template are byte-identical after tokenisation, they produce
//     identical 64-bit hashes. We take the hex of the full 64 bits
//     as the fingerprint, so equality on the fingerprint string is
//     equality on the template.
//
// What the package guarantees
//
//   - Determinism: the same input always produces the same Result.
//     No map iteration, no clock, no PRNG.
//   - Cluster purity: distinct templates produce distinct
//     fingerprints (with overwhelmingly high probability — a 64-bit
//     SimHash collision over a few thousand templates is unrealistic).
//   - Cluster recall: minor variation in numbers / UUIDs / timestamps
//     / paths inside an otherwise identical message does NOT change
//     the fingerprint.
//
// What the package does NOT do
//
//   - It does not store anything. Callers (the DLQ service layer)
//     persist the fingerprint + template into the dlq table at insert
//     time so cluster-rollup queries are cheap indexed GROUP BYs.
//   - It does not interpret semantics. "missing field X" and
//     "missing required X" template differently because they share
//     no leading word — and that's intentional, the human-readable
//     prefix is the cluster's identity for the operator.
//   - It does not normalise unicode beyond ASCII lowercasing. All
//     error strings in this codebase are ASCII or ASCII-ish; the
//     regexes are written against the ASCII subset.
//
// # Scoping by failing stage
//
// Two pipelines can fail identically at different stages: e.g.
// "missing field x" can come from a validate stage or from a
// transform stage that depends on the field. The operator needs to
// see those as separate clusters. FingerprintWithStage folds the
// stage name into the template + fingerprint to keep them apart.
package cluster
