---
title: It runs on the plan's own servers, and here is what that takes
short: Runs on the plan's own servers
tile: One 11 MB program. HTTPS only, data encrypted at rest, a standby, and records that survive a crash or a power cut. Speed measured and published.
area: Running it
icon: server
order: 11
description: What running Vaglio involves for a plan's IT team, how it protects patient data, how it was tested against crashes and power cuts, how fast it is on stated hardware, and the security checks anyone can see.
---
Most AI prior authorization is sold as a service: the vendor runs the software, and the plan sends it its members' records. Vaglio is the other kind. It is a program the plan runs on its own machines, under its own control, with the source code published so the plan, or anyone it asks, can read exactly what it does. This article is for the people who would run it: what it is, how it protects data, how it behaves when things go wrong, and how fast it is.

## One program

Vaglio is a single program file of about 11 MB. Inside it are the decision engine, the reviewers' console (a web application), the policy editor, the library, the reports and the audit log. It uses about 22 MB of memory when idle. It needs no database server, no separate web server and no runtime to install: you copy one file and start it.

It runs on Linux, macOS and Windows, on both Intel/AMD and Arm processors. There are three ways to install it:

- **A one-command installer** for Linux and macOS (as a system service) and Windows (as a startup task under a low-privilege account). It checks the download before running anything.
- **A container image**, built on a minimal base with no shell, running as a non-root user.
- **A Helm chart** for Kubernetes.

The [download page](/download/) has the commands.

## Patient data stays where the plan puts it

A prior authorization request contains patient health information. Vaglio keeps everything in one data folder the plan chooses: the request records, the audit log, the accounts.

- **Only Vaglio's own account can read it.** The server refuses to start if the folder can be read by other users. (On Windows, where access works differently, the installer makes it readable only by the system and its administrators.)
- **It can be encrypted at rest** with a key the plan holds, so a copied disk or backup is unreadable without it.
- **Nothing is sent out unless the plan turns it on.** Features that talk to outside services, such as a language model for reading documents, a fax service, or the [outside witnesses](/learn/receipts-anyone-can-check/) for the log, are off by default, and each sends only what it must. The witnesses, for example, receive only fingerprints.

## HTTPS only

Vaglio answers only over encrypted connections (HTTPS, using TLS 1.2 or later), on every address, the machine's own included. A plain, unencrypted request gets nothing but a redirect, and only to a name the server answers to. For the certificate, a plan can:

- use its own certificate, which Vaglio reloads when it changes;
- let Vaglio create a certificate authority of its own, restricted to this server's names, which the plan's machines trust once; or
- let Vaglio obtain a free certificate from Let's Encrypt by itself, opening the port it needs only while it fetches one. This has been tried against Let's Encrypt itself; the public demonstration's certificate is one Vaglio obtained.

The PAS server in front must present a secret token, which the installers create at random. Staff sign in with a password or the plan's own single sign-on. Repeated wrong passwords lock a name for 15 minutes, but a browser that has signed in as that person before keeps its own count, so a stranger who knows a name cannot lock its owner out.

## When things go wrong

A prior authorization system must never lose a decision it has already given. If it told a provider "approved", that approval must still be there after any crash. Vaglio is built and tested for that:

- **Recorded before answered.** Each decision and the request behind it are written to disk, and the disk is told to keep them, before the answer is sent. If that fails, no answer is given.
- **Killed under load.** On every change to the code, a test runs the server under load and kills it without warning. Every decision it answered is still there afterwards, its log still verifies, and it is answering again in about 0.2 to 0.4 seconds.
- **Power cuts, replayed.** A harsher test records every write the server makes to disk, then replays the disk as it would have been if the power had failed at each point where the server had asked for its data to be kept: 670 points on the Linux ext4 file system and 672 on XFS. At every one, the server started, its log verified, it made a new decision, and every decision answered before the "cut" was there. This test found a real problem in an earlier version, an accounts file that a power cut just after the first start could leave empty, and Vaglio now fixes it. It runs weekly on GitHub's machines.

The power-cut test takes the disk at its word. A disk that says it has saved data and then loses it defeats any software, and no test can fix that.

### A standby

A second machine can keep a live copy of the data, usually milliseconds behind the first. Every log entry is checked against the chain as it is copied, and a copy that has diverged is refused, never overwritten. To take over, the plan stops the standby and starts it as the server.

By default, an answer does not wait for the standby, so the last few decisions before a sudden failure might not have reached it; in our drills that was between 0 and 3 decisions. With the synchronous setting, every answer waits until the standby holds it, and a takeover loses nothing that was answered. That costs about 23% of the throughput on one machine. A standby that stops responding never stops decisions: each one waits at most the configured time, and the delay is recorded.

## How fast it is

Measured with `vaglio bench`, which anyone can run on their own hardware, in October 2026:

| | Apple M4, 16 GB | Amazon Lightsail "nano", 2 virtual CPUs, 512 MB, about $5 a month |
|---|---|---|
| The engine alone, one processor core | 95,000 decisions a second | 26,000 decisions a second |
| Whole requests, one at a time | 125 a second | 151 a second |
| Whole requests, several at once | 290 a second (10 at once) | 564 a second (8 at once) |

"The engine alone" is a decision with its full proof. A "whole request" includes reading the request, finding the evidence, deciding, and then saving both the request and the decision safely to disk before answering. Saving to disk sets the pace, not the engine. Requests arriving together share their disk saves, which is why several at once go faster. On macOS, a file being saved cannot be written to at the same time, which limits that sharing; Linux does not have that limit, which is why the $5 Linux server is faster here than the far more powerful Mac. For scale, 500 requests a second is 43 million a day, more than any single plan receives. These figures do not include the network or the PAS server in front.

## Security checks you can see

Every check below runs automatically, and its results are public on the repository's Actions page:

- **Tests** on Linux, macOS and Windows on every change, with Go's race detector on Linux.
- **Known vulnerabilities** in Vaglio's own code and in every package it uses (govulncheck, npm audit).
- **Code analysis** for security mistakes (GitHub's CodeQL, at its extended security level).
- **Fuzzing**: every part of the code that reads outside input, such as requests, fax images and policy files, is fed random and malformed data to find crashes. Fuzzing has already found and fixed one slow case.
- **A scan of the running console** by OWASP ZAP, a standard web security scanner.
- **The OpenSSF Scorecard**, which rates a project's security practices.
- **Installs on fresh machines**: the installers on all three systems, the container and the Kubernetes chart, tested on every change.

And the **releases can be checked**: every archive is built twice, on two kinds of machine, and published only if the two are identical, byte for byte. Each archive carries a signed statement of which code and which build produced it, and a list of every component inside it.

## Written down: the assurance pack

The repository includes an **assurance pack**: a threat model (who might attack what, and what stops them), the HIPAA Security Rule's technical safeguards mapped to the code that meets each one, a hazard log for the decision logic (ways a decision could go wrong and what prevents each), and measured recovery times. Two language models were asked to read it the way an outside auditor would; they found real gaps, which were fixed in the code and the pack, and their review is published too.

## What none of this is

To be plain: nothing above is a certification. Vaglio has no SOC 2 or HITRUST report, no penetration test by an outside firm, no business associate agreement, no service levels and no human review from outside BioDream. The assurance pack is written so that a plan's own security team, or an auditor it hires, has somewhere to start. [What Vaglio has not done yet](/learn/what-it-has-not-done/) lists the rest.

Next: [what Vaglio has not done yet](/learn/what-it-has-not-done/), all in one place.
