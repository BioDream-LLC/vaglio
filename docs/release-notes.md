# Vaglio v0.1.0

The first release. Early: not yet in production anywhere. Vaglio is source-available under PolyForm Shield 1.0.0.

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
