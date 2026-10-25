# Vaglio v0.2.0

Early: not yet in production anywhere. Vaglio is source-available under PolyForm Shield 1.0.0.

## What changed since v0.1.1

- **The library's policies now wait for the plan's clinicians.** Each built-in library policy decides only once the
  plan's own clinicians adopt its exact text in the console (name, NPI, credential and the text's hash recorded), as
  Medicare Advantage rules (42 CFR 422.137) and NCQA accreditation expect of the criteria a plan runs on. Until then its
  services are answered "decides nothing", so the PAS server in front decides or queues them as it would without
  Vaglio. Before, library policies decided as published unless `-require-adoption` was given.
  **To keep the old behaviour** (a demonstration or a trial), start the server with `-require-adoption=false`, or put
  `"requireAdoption": false` on a plan in a `-plans` file. The plan's own clinician-signed policies are unaffected.
- **Drafting**: a clause quoting a sentence that runs on through its list is accepted with its bullets left out; with
  it, a model drafted every one of the 60 LCDs in the study.
- Fixes to the tests and the demo's deployment script.

**Upgrading from v0.1.x**: stop the server, replace the program, start it, then either have the plan's clinicians adopt
the library policies it runs on (the console's "The plan's clinicians" panel lists them) or add
`-require-adoption=false`. Data written by v0.1.1 is read as is; do not go back to v0.1.0 on data v0.1.1 or later wrote.

## Checking what you downloaded

Each archive is listed in `SHA256SUMS`. Every archive also has a build provenance attestation from GitHub Actions,
signed through Sigstore, saying which commit and workflow built it:

    gh attestation verify vaglio-v0.2.0-linux-amd64.tar.gz --repo BioDream-LLC/vaglio

The archives are reproducible: the release workflow builds them twice, on x86 and Arm runners, and publishes nothing
unless the two are byte for byte the same. Building `deploy/release.sh v0.1.0` from the tagged commit with the Go
version in `go.mod` gives the same bytes on your machine.

`vaglio.cdx.json` and `console.cdx.json` are CycloneDX software bills of materials for the program and its console.

## What this release is not

Not certified by anyone, not penetration tested by a third party, and not validated clinically in production. The
README's "Status" section says plainly what has been tested against what.
