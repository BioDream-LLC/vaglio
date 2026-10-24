# Vaglio v0.1.0

The first release. Early: not yet in production anywhere. Vaglio is source-available under PolyForm Shield 1.0.0.

## What is in it

A prior-authorization decision engine a health plan runs on its own servers, beside a PAS server such as Perfuse:

- Policies in a small language of arguments; every decision comes with its proof, quoted from the policy's source, and
  a receipt anyone can check. Missing evidence never denies: the engine pends and asks for exactly what is missing.
- Evidence from the request's FHIR resources, the DTR answers, attached documents (read by a model or OCR as proposals
  a reviewer confirms), outside criteria services, the provider registry, and the DTR packages' own CQL, run by a
  CQL engine of Vaglio's own (`find cql`).
- A reviewers' console: the work queue, the clinician tier, what-if, replay, letters, appeals, intake by fax or phone.
- A library of policies drafted from public Medicare LCDs and NCDs, deciding as published, or only once the plan's
  clinicians adopt each one (`-require-adoption`).
- A link to the plan's own case-review (UM) system, decisions coming back under the reviewer's name.
- The decision log anchored outside the plan (`-anchor-every`): signed checkpoints, witnessed by OpenTimestamps,
  Sigstore Rekor or a time-stamping authority, with proofs on every receipt.
- Reports for CMS-0057 prior-authorization metrics, fairness, gold carding, and a shadow pilot beside the plan's own.

Off loopback the server needs a bearer token for the PAS server (`-token-env` or `-token-file`); the installers
make one.

## Checking what you downloaded

Each archive is listed in `SHA256SUMS`. Every archive also has a build provenance attestation from GitHub Actions,
signed through Sigstore, saying which commit and workflow built it:

    gh attestation verify vaglio-v0.1.0-linux-amd64.tar.gz --repo BioDream-LLC/vaglio

The archives are reproducible: the release workflow builds them twice, on x86 and Arm runners, and publishes nothing
unless the two are byte for byte the same. Building `deploy/release.sh v0.1.0` from the tagged commit with the Go
version in `go.mod` gives the same bytes on your machine.

`vaglio.cdx.json` and `console.cdx.json` are CycloneDX software bills of materials for the program and its console.

## What this release is not

Not certified by anyone, not penetration tested by a third party, and not validated clinically in production. The
README's "Status" section says plainly what has been tested against what.
