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
| | Reach the console from another site (CSRF, clickjacking) | Changes must be `application/json` (`sameOrigin` in `internal/server/server.go`): an HTML form cannot send that type, and a script on another site cannot send it without a CORS preflight, which the server never grants. An `Origin` or `Sec-Fetch-Site` header, when the browser sends one, must be this server's. `frame-ancestors 'none'`; cookies `HttpOnly`, `SameSite`; cross-origin isolation headers. GET changes nothing |
| | Guess passwords | PBKDF2-SHA256, 600,000 iterations (`internal/auth`); a name is locked after repeated failures; an address after 20 failures in 15 minutes; every failure audited |
| | Use a stolen session | Sessions idle out after 30 minutes and end after 12 hours; sign-out ends them on the server |
| | Sweep receipts for decisions | `/api/receipts/{code}` says nothing about the patient and is limited to 30 a minute per address |
| | Send a hostile document (PDF, TIFF, XML, PAS Bundle) | Parsers have size limits and are fuzzed in CI (`FuzzParse`, `FuzzRequest`, `FuzzReadMessage`, `FuzzEntraResponse`, `FuzzDecode`, `FuzzPages`); XML refuses DOCTYPEs; extractors run as separate programs on a private temporary file with a bare environment and a timeout |
| | Forge a single sign-on | SAML: signature checked against the configured certificate only, never one inside the response; tied to a request sent in the last 10 minutes, once; audience and time window checked (`internal/saml`). OIDC: issuer, audience, nonce, PKCE (`internal/oidc`) |
| A machine partner (PAS server, fax service, appeals system) | Act beyond its job | Each feed has its own bearer token, compared in constant time, accepted only on its own routes (the PAS token on the four `POST /api/pas/` routes, each answering about the request it was sent; the fax, appeals, outcomes and case-review tokens on their `/api/feeds/` routes); none of them reads cases or signs anyone in. Off loopback the server will not start without a PAS token (`-token-env` or `-token-file`, at least 16 characters; `-pas-no-token` only where something in front authenticates the PAS server); the installers make a random one, readable by the service alone. The exception is the standby's replica token, which reads everything (the log, the request Bundles, the accounts) because a standby is a copy: keep it like the data key |
| A signed-in person | Do more than their role | Roles viewer < reviewer < admin, plus scopes (author, intake, audit, appeals, a policy) checked on every route; only a clinician (NPI and credential recorded) may deny under `-clinician-denials`, sign off a policy, or rule on a shadow disagreement; an appeal is never decided by the person who denied |
| | Read patient data unseen | Reading a case is audited (`case.viewed`), as is the fairness report, which reads demographics |
| | Change a decision quietly | The log is append-only and each record carries the hash of the one before; the server refuses to start on a broken chain; `vaglio audit verify` checks it; receipts let anyone holding one check a decision against it |
| Someone with the disk (a backup, a stolen volume) | Read patient data | `VAGLIO_DATA_KEY` seals bundles, documents, the fax inbox and log lines with AES-256-GCM (`internal/seal`); the data directory must be mode 700 or the server will not start (Unix) |
| | Alter the log offline | The hash chain shows a record changed alone. A log rewritten whole, every hash recomputed, is caught by the anchors (`serve -anchor-every`, [anchoring.md](../anchoring.md)): the server signs checkpoints (a Merkle root over the records) and checks at every start that the log still matches them; witnesses outside the plan (OpenTimestamps, Rekor, an RFC 3161 authority, a regulator's endpoint) keep each checkpoint, so a rewrite after one cannot be hidden by rewriting the server's own copy too. Receipts carry an inclusion proof, checked offline (`vaglio anchor verify`) |
| | Rewrite the log and the anchors together | The checkpoints and their key are in the data directory (`DATA/anchor`), so whoever can rewrite the log there can rewrite them too. What stops that is the witnesses: they hold copies the plan does not control, and a receipt's holder can check its proof against them. Anchoring is off by default; with it on and no witness, only the server's own signed checkpoints stand against this attacker, and they do not: say so to whoever relies on it |
| | Leave a decision out of the log, or rebuild the log without it | Every decision's answer carries the server's signed promise of the record's place and hash in the log (`internal/anchor/promise.go`). Whoever kept the answer (the PAS server first) checks it against the checkpoint covering that place (`vaglio anchor verify -promise`); a log rebuilt without the record, even with the same keys and fresh checkpoints, contradicts the promise, and the two signatures are the proof. Not covered: a decision never answered, which carries no promise; and the PAS server must keep the answers for this to work |
| | Read the data key from memory | The key is held in the process's memory while it runs, as any key in use is; a memory dump or a debugger on the machine reads it. Who can log in to the machine is the plan's (below) |
| A model (document reader, drafter) | Invent evidence | A model's reading is a proposal: every finding must quote the document word for word (`reader.Gate`), and a reviewer confirms it; a model never decides. Approving from documents is opt-in (`-approve-from-documents`) and approve-only, with sampled checks |
| A CQL library (the plan's, a vendor's DTR package) | Change what a policy finds, or hang the server | A policy runs only the files it pins by SHA-256: a library or value set changed after sign-off stops the policy loading. CQL reads the request's resources only: no network, no files, no terminology service (value sets an author expands with `import-dtr -tx` are fetched once, then pinned like the libraries). Each evaluation is bounded (work, function depth, nesting) and fuzzed (`FuzzCompile`); an error or a null is not addressed, which never denies |
| The case-review (UM) system | Close a case without a reviewer, or as someone else | Its push feed has its own bearer token; a decision from it is held to the console's rules (a reason, the reviewer, a valid NPI; a clinician for escalated cases and denials); Vaglio never closes a case there on its own; every decision is recorded under `um:NAME` with the reviewer's name |
| A clinician adopting a policy | Adopt one text and have another decide | Adoption records the exact text's hash; only that text decides (`-require-adoption`); a new release's text is a new version, deciding nothing until adopted; two different clinicians where `-adoption-signatures 2` |
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
- No outside review of this threat model ([review.md](review.md) is the packet for one, and what a machine review found).
- Windows does not check the data directory's permissions (Windows reports no Unix modes); the installer sets its ACL.
- Sessions live in one process's memory: a restart signs everyone out (safe, but not a cluster).
- Lockout: five wrong passwords lock a name for 15 minutes only from where they came. A browser that has signed in as
  the name before carries a device cookie (signed by the server) and counts on its own; any other browser counts with
  the others at its address. So a stranger guessing at a name locks out only their own address, and the owner can
  still sign in from a browser they have used, or from any other address. What is left: guessing spread over many
  addresses (past 50 failures on one name in 15 minutes) locks every new browser out of that name for the rest of
  the window; the owner's known browsers and single sign-on still work. Someone on the same address as the guesser
  (a shared office address) is locked out with them, unless their browser is known.
- By default the standby is asynchronous: a primary lost under load can lose the decisions of the last milliseconds.
  `serve -standby-sync` makes each answer wait for the standby, and falls back to asynchronous, recorded, while the
  standby is down (measured: [recovery.md](recovery.md)).

## What has been run against it

OWASP ZAP's baseline scan of a running console, weekly and on every push (0 failures; warnings listed in the job);
CodeQL's security-extended queries for Go and TypeScript; govulncheck; fuzzing of every parser of outside input; the
recovery drill. Results are in the repository's Actions tab.
