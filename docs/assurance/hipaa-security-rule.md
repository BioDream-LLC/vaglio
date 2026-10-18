# HIPAA Security Rule: the technical safeguards, mapped to the code

45 CFR 164.312's technical safeguards, and where Vaglio meets each, or leaves it to the plan. This is a map for a
plan's security reviewer, not a compliance claim: HIPAA compliance belongs to the covered entity and how it runs the
software, and nobody outside BioDream has assessed this map. (R) is required, (A) addressable.

| Standard | What Vaglio does | Where | Left to the plan |
|---|---|---|---|
| **(a)(1) Access control** | Every route checks the session's role and scopes; machine feeds each have their own token | `internal/server/server.go` (`route`, `scoped`), `internal/auth` | Who gets which role |
| (a)(2)(i) Unique user identification (R) | One account per person, named; single sign-on maps the identity provider's user to one account | `internal/auth`, `internal/oidc`, `internal/saml` | Not sharing accounts |
| (a)(2)(ii) Emergency access procedure (R) | An administrator can make or reset accounts from the machine: `vaglio user add/reset -data DIR` | `cmd/vaglio` | The procedure and who holds machine access |
| (a)(2)(iii) Automatic logoff (A) | Sessions end after 30 minutes idle and 12 hours in all | `internal/auth` (`Idle`, `Max`) | |
| (a)(2)(iv) Encryption and decryption (A) | Data at rest sealed with AES-256-GCM under `VAGLIO_DATA_KEY`: request Bundles, documents, faxes, audit lines | `internal/seal`, `vaglio data seal` | Keeping the key apart from the data and its backups |
| **(b) Audit controls** (R) | Every decision, review, sign-in (and failed one), case read, report of demographics, policy change and setting change is a record in a hash-chained, append-only log; `vaglio audit verify` | `internal/audit`, `internal/cases` | Keeping the latest hash elsewhere; retention |
| **(c)(1) Integrity** | The hash chain: any changed, removed or reordered record breaks it, and the server will not start on a broken log. Request Bundles are stored by their SHA-256 | `internal/audit`, `internal/cases` | |
| (c)(2) Mechanism to authenticate ePHI (A) | Each decision's receipt is part of its record's hash, checkable by anyone holding it | `internal/receipt`, `/api/receipts/{code}` | |
| **(d) Person or entity authentication** (R) | Passwords hashed with PBKDF2-SHA256 (600,000 iterations), per-name lockout and per-address limits; or the plan's identity provider (OIDC with PKCE, SAML 2.0 with signatures checked against the configured certificate only); machines by bearer tokens compared in constant time | `internal/auth`, `internal/oidc`, `internal/saml`, `internal/server` | Multi-factor sign-in, through the plan's identity provider |
| **(e)(1) Transmission security** | HTTPS only, on every address; outbound calls (PAS server, fax services, Lob, criteria services) over HTTPS, with the plan's own authority accepted where configured (`-pas-ca`) | `internal/tlsserve` | Certificates from the plan's authority, or trusting Vaglio's |
| (e)(2)(i) Integrity controls (A) | TLS; webhooks signed with HMAC-SHA256; fax and document hashes kept | `internal/feed`, `internal/tlsserve` | |
| (e)(2)(ii) Encryption (A) | TLS 1.2 and later | `internal/tlsserve` | |

## Beyond the technical safeguards

The administrative (164.308) and physical (164.310) safeguards are the plan's: risk analysis, workforce training,
contingency plans, facility access, device and media controls. Vaglio helps with some of them:

- 164.308(a)(7) contingency plan: the standby (`vaglio standby`) and the recovery drill's measured numbers
  ([recovery.md](recovery.md)).
- 164.308(a)(1)(ii)(D) information system activity review: the audit log, and the console's history on every case.
- 164.310(d)(2)(iv) data backup and storage: the data directory is one directory; sealed, its copies are unreadable
  without the key.

A business associate agreement is between the plan and whoever operates Vaglio for it. BioDream does not operate
Vaglio for anyone today and offers no managed service.
