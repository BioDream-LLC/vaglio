# Vaglio v0.1.1

Early: not yet in production anywhere. Vaglio is source-available under PolyForm Shield 1.0.0.

## What changed since v0.1.0

- **Power cuts.** A new test (`tools/powercut`) replays what the disk was told to keep at every flush, as if the power
  went just after it; it found that a power cut just after the first start could leave an empty accounts file, so the
  server would not start. Every state file is now written durably (synced, renamed, directory synced); a record cut
  off mid-write is set aside on the next start instead of stopping it; anchors sign over synced records only. On ext4
  and XFS every one of about 670 replayed states starts, verifies and decides, and no answered decision is lost.
- **Faster where it counts.** Request Bundles are kept in pack files synced together, not one file and two syncs each:
  290 requests a second on an Apple M4 and 564 on a $5 Lightsail nano with a new Bundle per request, against about
  150 before. Bundles kept as single files by v0.1.0 are still read. A standby copies them several at a time.
  `vaglio audit verify` now checks every case's request too.
- **No more lockout by a stranger.** A browser that has signed in as a name before keeps its own count of failed
  sign-ins, so someone who knows the name cannot lock its owner out of it.
- **CQL.** 2,003 of HL7's 2,037 CQL conformance tests pass (Time, Long, Ratio, durations that are uncertain because a
  date is partial, the aggregate clause, interval and list operators, unit-aware quantities). `import-dtr -tx URL`
  expands value sets through a terminology server, `-libraries DIR` supplies included libraries a package lacks.
- **Let's Encrypt beside another program on port 80**: `-acme-http-on-demand` opens it only while fetching.
- **Drafting**: a clause quoting a sentence that runs on through its list is accepted with its bullets left out.

**Upgrading**: stop the server, replace the program, start it. A v0.1.1 server writes pack files that v0.1.0 cannot
read: do not go back to v0.1.0 on the same data.

## Checking what you downloaded

Each archive is listed in `SHA256SUMS`. Every archive also has a build provenance attestation from GitHub Actions,
signed through Sigstore, saying which commit and workflow built it:

    gh attestation verify vaglio-v0.1.1-linux-amd64.tar.gz --repo BioDream-LLC/vaglio

The archives are reproducible: the release workflow builds them twice, on x86 and Arm runners, and publishes nothing
unless the two are byte for byte the same. Building `deploy/release.sh v0.1.0` from the tagged commit with the Go
version in `go.mod` gives the same bytes on your machine.

`vaglio.cdx.json` and `console.cdx.json` are CycloneDX software bills of materials for the program and its console.

## What this release is not

Not certified by anyone, not penetration tested by a third party, and not validated clinically in production. The
README's "Status" section says plainly what has been tested against what.
