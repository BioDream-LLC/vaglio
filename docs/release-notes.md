# Vaglio v0.2.1

Early: not yet in production anywhere. Vaglio is source-available under PolyForm Shield 1.0.0.

## What changed since v0.2.0

- **Security fix (CQL).** An Integer or Long raised to a power too large to fit (such as `2000^9^9`) was computed
  before it was found too large, which could hang the server on one CQL expression. It is now null at once. Found by
  the fuzzer. Upgrade if you run CQL libraries you did not write.
- **Sign-in lockout.** A stranger guessing at a name now locks out only their own network address: an unknown browser's
  failures count per name and address (5 in 15 minutes). If guesses on one name come from many addresses (50 failures
  in 15 minutes), new browsers are locked out of that name; browsers the person has signed in from keep working.
- **Signed promises.** Every decision's PAS answer, webhook event and export line carries a promise signed with the
  checkpoint key: this record, at this place in the log. `vaglio anchor verify -promise ANSWER.json RECEIPT.json` says
  BROKEN PROMISE when a later log contradicts it, so a rebuilt log is caught even for a decision whose receipt was
  never anchored outside.
- **CQL conformance.** 2,003 of the HL7 suite's 2,009 tests for CQL 1.5.3 pass (was 1,981), with about 20 engine fixes
  (interval bounds, decimal comparison, timing phrases, queries over several sources, Children and Descendents). A
  policy whose `find cql` lines touch these may now find differently; replay it over past cases before upgrading
  (`vaglio replay`, or the console's time machine).
- **Synchronous standby is faster.** The standby is woken by each new record, and an answer waits only for its own
  case's records: 7 to 10% more decisions a second than v0.2.0 in sync mode on our test machine, none lost.
- **Draft twice.** `vaglio draft -twice` and `serve -draft-twice` draft each document a second time (the same model or
  `-second-model`) and compare the two readings on every combination of evidence; disagreements head the draft and
  show in the console.
- **Policies of up to 40 pieces of evidence** (was 16), still checked on every combination exactly: past 16 by a
  search that counts whole families of combinations at once. A decision whose search for what could change it runs out
  of budget is cautious: a denial pends, asking for everything not documented (`cautious` in the result). The policy
  map shows a large policy 16 pieces at a time, the rest held present or absent as the reader sets them; `vaglio
  readings` compares up to 62 pieces between two readings; the drafting guide tells models 40.
- The LCD study's comparisons of two readings now cover the pairs that were too large before (docs/lcd-study.md).

**Upgrading from v0.2.0**: stop the server, replace the program, start it. Data written by v0.2.0 is read as is. New
fields in answers and reports (`promise`, `cautious`, `incomplete`) are additions; nothing was removed or renamed.

## Checking what you downloaded

Each archive is listed in `SHA256SUMS`. Every archive also has a build provenance attestation from GitHub Actions,
signed through Sigstore, saying which commit and workflow built it:

    gh attestation verify vaglio-v0.2.1-linux-amd64.tar.gz --repo BioDream-LLC/vaglio

The archives are reproducible: the release workflow builds them twice, on x86 and Arm runners, and publishes nothing
unless the two are byte for byte the same. Building `deploy/release.sh v0.2.1` from the tagged commit with the Go
version in `go.mod` gives the same bytes on your machine.

`vaglio.cdx.json` and `console.cdx.json` are CycloneDX software bills of materials for the program and its console.

## What this release is not

Not certified by anyone, not penetration tested by a third party, and not validated clinically in production. The
README's "Status" section says plainly what has been tested against what.
