# Mutation testing: known equivalent / uncoverable mutants

Gate: `.gremlins.yaml` (`efficacy: 99`, `mutant-coverage: 93`), run per domain
package by `make mutation` (CI `mutation-fast`) and on `./internal/domain` by
`make mutation-full` (CI `mutation`). gremlins v0.6.0.

Measured 2026-11-15: 0 lived mutants in every package; the only non-killed
mutants are the two below.

| Package | File:line | Mutators | Why it is not killable |
|---|---|---|---|
| `internal/domain/policy` | `policy.go:34` | `INVERT_NEGATIVES`, `ARITHMETIC_BASE` (both NOT COVERED) | `const MaxHintTTLSeconds int64 = 1<<31 - 1`. Go's cover tool instruments statements, not constant declarations, so the line never appears in the coverage profile and gremlins reports NOT COVERED. Behaviour is asserted by the policy tests (the TTL ceiling boundary), but the profile cannot show it. |

Effect on the gate: policy measures 100% efficacy and 93.55% mutator coverage
(29 killed, 2 not covered), so `mutant-coverage` is 93, strictly below 93.55.
The other packages measure 100% / 100%. Raising `mutant-coverage` to 99 needs
the constant to become a statement-level expression (a production change, kept
out of this test-only ratchet) or the two mutants to be excluded.
