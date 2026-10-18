# Threat model

What Vaglio protects, from whom, how, and what it leaves to the plan running it. It is written from the code; each
control names where it lives. It has not been reviewed by an outside security firm, and Vaglio has not been penetration
tested by one. What has been run is listed at the end.

## What is protected

| Asset | Where it is | Why it matters |
|---|---|---|
| Patient records in requests | `DATA/bundles` (each request Bundle, by its SHA-256), `DATA/audit.jsonl` | Protected health information |
| Decisions and their proofs | the audit log | A wrong or altered decision harms a patient, and is the plan's legal record |
| Policies | the policy directory, `DATA/versions` | A changed policy changes every decision after it |
| Accounts and sessions | `DATA/users.json`, memory | Reviewers' and clinicians' authority to decide |
| Tokens and keys | environment variables named by `-*-token-env`, `VAGLIO_DATA_KEY`, `DATA/tls` | Machines' access; the data key opens everything |

## Who might attack, and how

| Who | What they might try | What stands in the way |
|---|---|---|
| Someone on the network | Read or change traffic | HTTPS only on every address (`internal/tlsserve`); plain HTTP gets a redirect to an allowed host, never data; HSTS with `-secure` |
| | Reach the console from another site (CSRF, clickjacking) | Changes must be `application/json` with a same-origin `Origin` (`sameOrigin` in `internal/server/server.go`); `frame-ancestors 'none'`; cookies `HttpOnly`, `SameSite`; cross-origin isolation headers |
| | Guess passwords | PBKDF2-SHA256, 600,000 iterations (`internal/auth`); a name is locked after repeated failures; an address after 20 failures in 15 minutes; every failure audited |
| | Use a stolen session | Sessions idle out after 30 minutes and end after 12 hours; sign-out ends them on the server |
| | Sweep receipts for decisions | `/api/receipts/{code}` says nothing about the patient and is limited to 30 a minute per address |
| | Send a hostile document (PDF, TIFF, XML, PAS Bundle) | Parsers have size limits and are fuzzed in CI (`FuzzParse`, `FuzzRequest`, `FuzzReadMessage`, `FuzzEntraResponse`, `FuzzDecode`, `FuzzPages`); XML refuses DOCTYPEs; extractors run as separate programs on a private temporary file with a bare environment and a timeout |
| | Forge a single sign-on | SAML: signature checked against the configured certificate only, never one inside the response; tied to a request sent in the last 10 minutes, once; audience and time window checked (`internal/saml`). OIDC: issuer, audience, nonce, PKCE (`internal/oidc`) |
| A machine partner (PAS server, fax service, appeals system) | Act beyond its job | Each feed has its own bearer token, compared in constant time; none of them can read cases or sign anyone in |
| A signed-in person | Do more than their role | Roles viewer < reviewer < admin, plus scopes (author, intake, audit, appeals, a policy) checked on every route; only a clinician (NPI and credential recorded) may deny under `-clinician-denials`, sign off a policy, or rule on a shadow disagreement; an appeal is never decided by the person who denied |
| | Read patient data unseen | Reading a case is audited (`case.viewed`), as is the fairness report, which reads demographics |
| | Change a decision quietly | The log is append-only and each record carries the hash of the one before; the server refuses to start on a broken chain; `vaglio audit verify` checks it; receipts let anyone holding one check a decision against it |
| Someone with the disk (a backup, a stolen volume) | Read patient data | `VAGLIO_DATA_KEY` seals bundles, documents, the fax inbox and log lines with AES-256-GCM (`internal/seal`); the data directory must be mode 700 or the server will not start (Unix) |
| | Alter the log offline | The hash chain shows it; keeping the latest hash elsewhere (`vaglio audit verify` prints it) shows a log rewritten whole |
| A model (document reader, drafter) | Invent evidence | A model's reading is a proposal: every finding must quote the document word for word (`reader.Gate`), and a reviewer confirms it; a model never decides. Approving from documents is opt-in (`-approve-from-documents`) and approve-only, with sampled checks |
| The software supply chain | Ship altered code | No Go dependencies beyond Perfuse (BioDream's own); the console's packages pinned; govulncheck, npm audit and CodeQL in CI; release archives built twice and compared, with SBOMs and GitHub build provenance |

## Left to the plan

- Keeping `VAGLIO_DATA_KEY` and the tokens outside the data directory and its backups.
- The network: which addresses reach Vaglio, and a firewall in front of it.
- The operating system, its patches, and who can log in to the machine.
- Backups of the data directory, kept where the data key is not.
- Identity: who gets an account, and removing it when they leave (single sign-on helps).
- Trusting Vaglio's own certificate authority (`DATA/tls/ca.crt`), or giving it the plan's certificate.

## Known gaps

- No independent penetration test or security audit.
- Windows does not check the data directory's permissions (Windows reports no Unix modes); the installer sets its ACL.
- Sessions live in one process's memory: a restart signs everyone out (safe, but not a cluster).
- The standby is asynchronous: a primary lost under load loses the decisions answered since the standby's last copy
  (measured: [recovery.md](recovery.md)).

## What has been run against it

OWASP ZAP's baseline scan of a running console, weekly and on every push (0 failures; warnings listed in the job);
CodeQL's security-extended queries for Go and TypeScript; govulncheck; fuzzing of every parser of outside input; the
recovery drill. Results are in the repository's Actions tab.
