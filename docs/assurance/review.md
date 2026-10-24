# Review of the assurance pack

**Nobody outside BioDream has reviewed this pack.** This page holds two things meanwhile: the record of a machine review
(two language models asked to read the pack as an outside auditor would, and what was done about each finding), and the
packet an outside reviewer would start from. A machine review is not an independent review. It found real gaps, which are
fixed below, but it reads only what it is given, cannot run anything, and is not accountable for what it misses.

## Machine review, 24 October 2026

The whole pack (this directory and [anchoring.md](../anchoring.md), about 5,000 words) was sent with one instruction:
review it as a skeptical outside auditor, and list concrete findings (the claim, what is wrong or missing, why it matters,
what would resolve it), at most 25, most serious first, no praise. Two models through OpenRouter, temperature 0:
DeepSeek V4 Pro (25 findings) and Llama 3.3 70B (25). Only the pack was sent, no code and no data; the repository is
public anyway.

Llama's findings almost all restate what the pack already lists as left to the plan (roles, key custody, networks,
backups, retention) or as known gaps (no penetration test, asynchronous standby, Windows permissions), asking for
"guidance". None changed anything. DeepSeek's checked the pack's claims against each other and against what the
controls would actually stop, and every one was checked against the code. Each, and what was done:

| # | Finding, in short | Verdict | Done |
|---|---|---|---|
| F1 | The quote gate checks that a quote is present, not what it means | Right | Hazard log H4 says so: a true quote can be read out of context, which is what the reviewer, with the document beside it, is for |
| F2, F3 | The threat model implies a whole-log rewrite is caught, but anchoring is off by default; the HIPAA integrity row omits the limit | Right | Both now say anchoring is off by default and the hash chain alone does not show a whole rewrite |
| F4 | A stolen volume holding the key and the data yields everything | Already covered | Key custody is listed as the plan's; unchanged |
| F5 | Sessions are process-local, unstated in the HIPAA row | Right | The automatic logoff row says a restart or a takeover signs everyone out |
| F6 | The automatic approval path has no reviewer before the decision | Right | H4 now describes that path apart: approve-only, sampled checks after |
| F7 | How a library's pin is trusted at sign-off is unspecified | Partly right | The pin is in the policy's text, which the sign-off and the adoption hash cover, and a changed file stops the policy loading (`TestPins`). Still open: the console shows the pin lines, not the library's text, so the signer must read the file itself |
| F8 | "No machine token can read cases" is unverified | Right, and worse than the finding | The replica token reads everything by design (now stated). Checking the routes found a **real gap: with no `-token-env`, `/api/pas/` took requests from anyone who could reach the server, off loopback too**. Fixed: off loopback the server will not start without a PAS token of 16 characters or more (`-token-file` added; `-pas-no-token` to say something in front authenticates the PAS server); the installers make a random token file readable by the service alone, the chart a Secret. `TestPASTokenOffLoopback` |
| F9 | A request with no `Origin` header may pass the CSRF check | Wrong, but the pack did not say why | The real control is the content type: a form cannot send `application/json` and another site's script cannot without a CORS preflight, which is never granted. The threat model now says so |
| F10 | The per-name lockout lets someone lock an account | Right | Closed for the browsers an account already uses (device cookies, `internal/auth/device.go`, tests `TestKnownBrowserIsNotLockedOut`, `TestKnownBrowserSignsInDespiteLock`); a new browser can still be locked out for 15 minutes, listed as a known gap |
| F11 | The data key is in memory while the server runs | Right | A threat row says so; who can log in to the machine is the plan's |
| F12 | SAML replay within the window | Already covered | The request ID is used once; `samlsso_test.go` signs in twice with one response and is refused |
| F13 | "Not decided by the denier" by account name only | Right | **Fixed in code**: the denier's NPI is recorded with the appeal, and an account with that NPI cannot decide it (`TestWorkQueue`, a second account with the same NPI) |
| F14 | The enumeration of missing evidence has no stated bound | Right | H1 now states it: at most 16 pieces of evidence, refused at parse, so every combination is tried |
| F15 | How a higher policy "may apply" is known | Already covered | `internal/precedence` tests (`TestChoose`, `TestConflict`) |
| F16 | Is the sync an fsync, of the file and the directory? | Right | Files were fsynced; **their directories were not**, so a new Bundle's name could be lost to a power cut. Fixed: the directory is synced after each new Bundle and when the log is created. Cost measured: about a sixth of whole-request throughput on an M4 (README "Speed"). H8 says `kill -9` is not a power cut: no power-cut test has been run |
| F17 | "No Go dependency beyond Perfuse" is unverified | Right | `TestOnlyPerfuse` fails on any other module in `go.mod` or `go.sum` |
| F18 | Are a model's proposals kept, apart from the reviewer's confirmation? | Already covered | Each reading is its own audit record (`case.read`), the confirmation another |
| F19, F23 | Where webhook and machine tokens are kept | Already covered | Environment variables or a private token file, never the data directory; custody is the plan's |
| F20 | Does the adoption hash cover the CQL a policy runs? | Already covered | The pins are in the adopted text; a changed library does not load (`TestPins`) |
| F21 | Synchronous standby falls back and the hazard returns | Right | H12 says so |
| F22 | The checkpoints sit in the data directory beside the log | Right | The threat model says only witnesses stand against someone who can write the data directory |
| F24 | Who may read the fairness report | Already covered | Reviewers and up; a viewer gets 403 (`features_test.go`) |
| F25 | What a CQL define stopped at its limit does | Right that it was untested | `TestStoppedIsNotAddressed`: no finding, a note, never a denial |

Also from this pass, before the review: the CQL engine now has limits on work, function depth and nesting, and the
fuzzer added for it (`FuzzCompile`) found a concept that names itself recursing without end, fixed.

## For an outside reviewer

What would make this a review: a person or firm with security, HIPAA and clinical-safety competence, independent of
BioDream, reading the pack against the code and running it, and saying what they found under their own name. Where to
start:

- **Run it.** `make check` (every Go test, the console's tests, the policy checks); `go run ./tools/drill` (the
  recovery drill); `conformance/inferno/run.sh` (the Inferno PAS kit with Perfuse in front). CI runs all of these,
  plus CodeQL, govulncheck, fuzzing and ZAP, in the repository's Actions tab.
- **Claims to try to break**, most consequential first:
  1. Missing evidence never denies (`internal/decide`, H1): find a policy and a request where a denial rests on
     evidence nobody addressed.
  2. A decision cannot be changed or hidden after it is answered (H7, H8, `internal/audit`, `internal/anchor`):
     change one, with access to the machine, without the chain, the anchors or a witness showing it.
  3. A role cannot do more than it may (threat model, `internal/server`'s `route` and `scoped`): reach a case, a
     report or a change from a role or token that should not.
  4. A model, OCR, or CQL cannot introduce evidence a reviewer does not see (H4, H5, H14).
  5. The HIPAA 164.312 mapping: each row against the code it names.
- **Out of scope here, the plan's**: its network, machines, accounts, key custody, backups, and clinical sign-off of the
  policies it runs.

Findings are welcome privately, through the repository's Security tab ("Report a vulnerability"); BioDream will
record them here, with what was done.
