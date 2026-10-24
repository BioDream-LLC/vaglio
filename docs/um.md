# Linking Vaglio to the plan's case-review system

A health plan's nurses and physicians already work prior authorization cases in a utilization management (UM) system.
With `serve -um FILE`, Vaglio fits into that system instead of asking them to move to its console:

- **Out.** Every case Vaglio pends is opened in the UM system. That covers missing evidence, a policy that cannot settle
  the request, and a denial that only a clinician may make. The case carries:
  - why it waits;
  - what the record shows, each finding quoted;
  - what it does not address;
  - what would approve it;
  - the proof, step by step;
  - the receipt;
  - when the decision is due;
  - a link back to the case in Vaglio;
  - all of it as a PDF.
- **In.** The reviewer decides the case there. The decision comes back, pushed by the UM system to `POST /api/feeds/um`
  or polled by Vaglio. It closes Vaglio's case as a review recorded under the reviewer's name, NPI and credential, with
  the system's name and its case number. The audit log, receipts, letters, CMS metrics, fairness report and delivery to
  the PAS server then work as they do for a decision made in the console.

The UM system stays where the human decision is made. Vaglio opens cases there and reads what people decided. It never
decides a case there, never closes one, and never writes a decision back.

## The rules a decision from outside must pass

They are the console's rules:

- A decision needs a reason and the reviewer's name.
- An NPI, when sent, must be a valid NPI.
- A case sent to a physician, or a denial the engine only proposed, is recorded only from a clinician. So is any denial
  on a server started with `-clinician-denials`. A clinician here means the decision carries both an NPI and a
  credential.
- A case decided already, or replaced by a later request, refuses a second decision. The feed's answer says why.
- The same decision sent twice is recorded once.

While a case is open in the UM system, the console shows the link and refuses to decide the case, because two places
to decide one case is how decisions get lost. `"reviewHere": true` in the file allows both.

## The file

Secrets are named by environment variable, never written in the file. Plain http is allowed only to this machine.

| Field | What it is |
|---|---|
| `name` | The system's name, as cases and the log record it (default `um`). |
| `connector` | `rest` (any JSON API, wired by field maps) or `fhir` (FHIR R4 Task). |
| `url` | Where cases are opened: the REST endpoint, or the FHIR server's base. |
| `tokenEnv`, `headerEnv`, `headers` | A bearer token, other secret headers (header name to variable), fixed headers. |
| `ca` | Extra certificate authorities for the system's certificate. |
| `console` | Vaglio's address as the plan's reviewers reach it; the case links are built from it. |
| `pollEvery` | Ask about open cases this often (`5m`, at least `10s`). Without it, decisions come only to the feed. |
| `reviewHere` | Let the console decide linked cases too. |

`-um-token-env VAR` names the token the UM system pushes decisions with, at least 16 characters. A server with `-um`
needs that token, `pollEvery`, or both, or decisions could never come back. A shadow pilot (`-shadow`) refuses `-um`,
since it must answer no one. With `-plans`, each plan has its own `"um"` and `"umTokenEnv"`.

### `rest`: any system with a JSON API, without code

- `fields` maps the body sent: a target field (dotted for nested objects) to a part of the packet, or `=text` for a
  fixed value. Without `fields`, the whole packet is sent as it is.
- The packet's parts are `case`, `link`, `receipt`, `opened`, `due`, `expedited`, `drug`, `policy`, `policyVersion`,
  `policyTitle`, `service`, `claim`, `request`, `patient`, `provider`, `providerNpi`, `line`, `patientState`,
  `reason`, `why`, `proposedDenial`, `clinicianOnly`, `missing`, `toApprove`, `findings`, `proof` and `proofPdf` (the PDF,
  base64).
- A name that is not one of them is refused when the server starts. A part a case has none of (no missing evidence,
  say) is left out.
- `refField` is where the answer gives the new case's id, and `urlField` its address. Without `refField`, the id is the
  last part of the answer's `Location` header.
- `decisionUrl` (`{ref}` stands for the case's id) is polled with GET. `decisionFields` maps a decision's parts (`case`,
  `ref`, `decision`, `reason`, `reviewer`, `npi`, `credential`, `decided`) to where the system's JSON has them.
- `decisionValues` maps the system's words to `approve` or `deny` (`{"APPROVED": "approve"}`). Any other value is a case
  still in review.
- Pushed decisions are read with the same map: one JSON object, or an array.

`examples/um.json` wires `examples/um-standin`, a stand-in system with an API unlike Vaglio's own. It keeps cases in
memory, shows them on one page, and pushes each reviewer's decision to Vaglio:

```sh
UM_KEY=... UM_PUSH=... go run ./examples/um-standin -vaglio https://127.0.0.1:8740 -vaglio-ca DATA/tls/ca.crt
UM_KEY=... UM_PUSH=... vaglio serve ... -um examples/um.json -um-token-env UM_PUSH
```

### `fhir`: a FHIR R4 Task

Vaglio posts a Task to `URL/Task`. The Task's shape is Vaglio's own convention, not a published implementation guide's,
so the plan's system must read and write it as described here.

- **Out.** The Task carries:
  - `identifier`, system `urn:vaglio:case`, holding Vaglio's case id;
  - status `requested`, intent `order`, priority `stat` for an expedited request;
  - `description`, why the case waits;
  - `restriction.period.end`, when the decision is due;
  - inputs named by `type.text`: `receipt`, `link`, `policy`, `service`, `reason`, `proposed-denial`, `missing` (one
    each) and `proof` (the PDF).
- **In.** A reviewer completes it: status `completed`, `owner` the reviewer (display, and an identifier in the NPI
  system), and outputs `decision` (`approve` or `deny`), `reason`, and optionally `reviewer-npi` and
  `reviewer-credential`.

Vaglio polls `URL/Task/{id}`, or the system pushes the completed Task, or a subscription notification Bundle of
Tasks, to the feed. A Task completed with no decision output is not a decision, and nothing is recorded.

## Named systems

Before building any named connector, we checked what each vendor publishes:

| System | Published API Vaglio could build against |
|---|---|
| HealthEdge GuidingCare | Not public. Its documentation is under subscription agreements. |
| ZeOmega Jiva | None found in public. |
| Medecision Aerial | None found in public. |
| Cognizant TriZetto (Facets, QNXT) | Not public. Behind the vendor's partner programs. |
| Salesforce Health Cloud | Its utilization management object model (care requests and their reviewers) is published, but not every field could be read from the public pages. |
| Epic payer platform | Payer-to-provider exchange goes through the Da Vinci standards (PAS, CDex), which Perfuse already speaks. |

So no named connector is built. A plan's system with a JSON API is wired with a `rest` file, and one with FHIR with
`fhir`. A named connector waits for a plan's sandbox of that system.

## What is tested, and what is not

- **Tested** against a stand-in system with its own API (`internal/server/um_test.go`, `examples/um-standin`):
  - cases opened with the proof and the PDF;
  - polled and pushed decisions;
  - the clinician rules;
  - a second decision refused;
  - a repeat recorded once.
- **Tested** against a stand-in FHIR server (`internal/um/um_test.go`): Task out, completed Task in, by poll and as a
  subscription Bundle.
- **Not tried** against any real UM system. That needs a plan's sandbox.
- **Not built:**
  - Vaglio does not tell the UM system when a case is replaced by a later request (the decision, when it comes, is
    refused, and the feed says why); the signed webhook (`-webhook`) carries the replacement for a plan that wants it;
  - documents that arrive after the request are not sent on to the case there;
  - OAuth client credentials: a token from the environment only.
