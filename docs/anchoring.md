# Anchoring the decision log outside the plan

Vaglio's audit log is hash-chained: every record carries the hash of the one before it, and a receipt is the start of a
decision record's hash. That catches an edit to one record. But the plan holds the whole chain, and whoever can write
the file could rewrite every record from some point on and recompute every hash after it. The result would still be a
chain that verifies.

Anchoring adds witnesses outside the plan. It is not a blockchain of Vaglio's own. It is signed checkpoints, and
existing public services that record them.

## What it does (`serve -anchor-every 1h`)

- **Signed checkpoints.** At the interval, the server signs a checkpoint of the log with its own Ed25519 key. A
  checkpoint states the log's size, the root of a Merkle tree over its records (RFC 6962, as Certificate Transparency
  uses), and the time. It is kept in `DATA/anchor/checkpoints.jsonl` and recorded in the audit log itself.
  - **Each extends the last.** Every checkpoint must extend the one before it. A consistency proof shows it does, and
    `GET /api/anchor/consistency?from=SIZE&to=SIZE` hands one to anyone who asks.
  - **Checked at start.** On every start, the server checks that the log still has every root it signed. A log
    rewritten after a checkpoint does not. The server then logs a warning, signs no further checkpoints, and every
    receipt page shows the problem in red.
- **Witnesses, chosen by the plan.** Each checkpoint can go to one or more of these, and none is on by default:
  - `-anchor-ots`: OpenTimestamps (free). Two public calendars commit the checkpoint's digest to Bitcoin within hours.
    The answer is kept as a standard `.ots` file: the OpenTimestamps client upgrades it once the block is mined
    (`ots upgrade`) and verifies it (`ots verify`).
  - `-anchor-tsa URL,...`: RFC 3161 time-stamping authorities (DigiCert's, FreeTSA's, the plan's own). The signed token is
    kept as a `.tsr` file, verified with the authority's certificate (`openssl ts -verify`). Vaglio checks the token
    is over the checkpoint's digest and reads its time; it does not check the authority's signature itself.
  - `-anchor-rekor https://rekor.sigstore.dev`: Sigstore's public transparency log. Each checkpoint's digest goes in as
    a `hashedrekord` entry, signed with an ECDSA P-256 key of the plan's own (`DATA/anchor/rekor.key`; Rekor checks a
    signature over a digest, which the Ed25519 checkpoint key cannot make). Rekor's answer is kept as a `.json` file:
    the entry, its log index and its signed entry timestamp, which the server checks against Rekor's published key.
    Anyone can find the entry again with `rekor-cli get --log-index N`.
  - `-anchor-witness URL` (and `-anchor-witness-token-env`): an https endpoint a regulator or partner runs, which is
    posted each signed checkpoint and keeps it.
- **Receipts carry a proof.** A receipt's answer (`GET /api/receipts/CODE`, and the public receipt page) carries:
  - the record's inclusion proof against the first checkpoint covering it;
  - that checkpoint, signed;
  - what its witnesses answered.

  Whoever holds the receipt checks it offline, with the plan's published key:

  ```sh
  curl -s https://vaglio.plan.example/api/receipts/VG-1A2B-3C4D-5E6F-7A8B > receipt.json
  vaglio anchor verify -key PLANKEY receipt.json
  ```

  The same command checks a consistency proof (two checkpoints, the later extending the earlier).

  The witnesses' files are at `GET /api/anchor/witness/{file}`, for their own tools. `GET /api/anchor` is the key and
  the latest checkpoint, `GET /api/anchor/checkpoints` is every checkpoint, and an administrator can sign one at once
  with `POST /api/anchor/checkpoint`.

## Only hashes leave the server

What goes out is a checkpoint (a size, a root, a time, a signature) or its SHA-256 digest. No record and no patient data
leaves the server.

A record's leaf in the tree is not its plain hash. It is a commitment: the hash of a secret salt and the record's hash.
An inclusion proof shows a receipt's holder the leaves next to theirs, and without the salts nobody can test a guess
about another patient's record against them. Each record's salt is derived from a key in `DATA/anchor/salt.key` and is
shown only with that record's own receipt.

`DATA/anchor/signing.key` is the checkpoint key. The plan publishes its public half (the server logs it at start, and
`GET /api/anchor` gives it) so receipts can be tied to the plan. Losing it means new checkpoints are signed with a new
key; the old ones still verify with the old public key.

## Promises: a record left out is provable

A checkpoint alone cannot show a record is missing: a server could answer a request and never log it, or rebuild
its log without it before the next checkpoint, keeping its keys so the new checkpoints look like the old ones. So
every decision's answer (`POST /api/pas/decide`) carries a **promise**, signed with the checkpoint key:

```
vaglio promise v1
<the log's name>
<the record's number in the log>
<the record's hash>
<its leaf in the Merkle tree>
<the time>
```

The PAS server in front, or anyone it passes the answer to, keeps it. The signed webhook and the NDJSON export
(`GET /api/export/events`) carry the same promise on each decision's record, so a plan's data warehouse, a
regulator's feed or a partner holds one too, outside the plan's own servers. Later, the receipt's answer gives the record's
proof against the checkpoint that covers it, and

```
$ vaglio anchor verify -key PLAN-KEY -promise answer.json receipt.json
```

checks that the checkpoint's tree holds the promised leaf at the promised place. If it does not, the server has
signed two statements that contradict each other, and the command says `BROKEN PROMISE`: that is proof, signed by the
server itself, that the record was changed or left out. A server that refuses to give the proof at all is refusing
something it promised, which is plain to see. This is what certificate transparency logs give with their signed
certificate timestamps (RFC 6962).

Making a promise costs one Ed25519 signature and no read of the log, so every answer carries one.

## What it proves, and what it does not

- **It proves** that the records up to a checkpoint have not changed since it was signed, and, with a witness, since
  the time the witness recorded. A plan cannot rewrite its history after a witnessed checkpoint without the rewrite
  failing to match what the witness holds.
- **It does not prove:**
  - that a decision was right;
  - anything about a decision nobody was ever told of. A decision whose answer was given carries a promise, so leaving
    it out is provable; a decision never answered at all carries none. Records made after the last checkpoint are
    covered only by the next one (their promises wait for it).
- **The witnesses' own trust is theirs.** Bitcoin's for OpenTimestamps, Sigstore's for Rekor, the authority's
  certificate for RFC 3161, the endpoint's operator for the last kind.

## What is tested

- `internal/merkle`: the tree hashes of RFC 6962's reference leaves, and every inclusion and consistency proof in trees
  up to 40 leaves (a changed leaf or a wrong root fails).
- `internal/anchor`:
  - a receipt's proof verifies, and fails with another salt or another key;
  - a later checkpoint extends an earlier one;
  - a promise is kept by the honest log's proof, and broken by a log rebuilt without the record (same keys, old
    checkpoints thrown away, every hash fresh), or by the old leaf on the new tree (`TestPromises`); the server's
    answers carry one (`TestAnchoredReceipt`) and `vaglio anchor verify -promise` reports a broken one;
  - a log rewritten after a checkpoint, with every hash recomputed and a chain that verifies on its own, is caught at
    start;
  - the witnesses, against stand-ins.
- Against the real services (2026-10-23):
  - OpenTimestamps (`a.pool` and `b.pool`): the `.ots` file reads correctly in the official OpenTimestamps client,
    pending in Bitcoin;
  - FreeTSA: its token verifies with `openssl ts -verify`;
  - DigiCert's authority: its token reads correctly;
  - a server run with `-anchor-every 1m -anchor-ots -anchor-tsa https://freetsa.org/tsr`: a receipt's answer verified
    offline with `vaglio anchor verify -key`.
- Rekor: against a stand-in that checks the entry's signature as Rekor does and signs its answer (a timestamp from another
  key, or an entry over another digest, is refused); and once against the real `rekor.sigstore.dev` (2026-10-24, entry
  3440703923, its signed entry timestamp verified with Rekor's key).
