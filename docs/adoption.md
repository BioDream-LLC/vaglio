# The plan's clinicians approve the policies it runs on

Vaglio's library policies are drafted from public Medicare documents (LCDs and NCDs) and public Medicaid criteria. By
themselves they decide nothing: a plan's own physicians must approve whatever it actually runs on (Medicare Advantage:
a utilization management committee reviewing its policies at least yearly, 42 CFR 422.137; NCQA: practitioners
involved in adopting and reviewing criteria). A plan that reads a rule differently overrides one with its own
clinician-signed version instead.

## Adoption (on by default; `serve -require-adoption=false` turns it off)

A library policy decides only once the plan's clinicians have adopted its exact text.
Until then it decides nothing here: the PAS decision service answers "decides nothing" (404) for its services, so the
PAS server answers as it would without Vaglio, and advice before submitting says the same.

- **Who adopts.** A clinician adopts: an account with an NPI and a credential (`vaglio user clinician`). The policy's
  screen has a button for it. Each adoption is an audit record of:
  - the clinician's name, NPI and credential;
  - the capacity they act in ("medical director", "orthopedic surgeon");
  - the committee's minutes or reference;
  - the date;
  - the SHA-256 of the exact text adopted.
- **Several clinicians.** `-adoption-signatures 2` needs two different clinicians, counted by NPI: a specialist and the
  medical director, say.
- **A stale text is refused.** The console sends the hash of the text the clinician read. If that text has changed
  since, the adoption is refused.
- **Withdrawal.** A clinician withdraws an adoption, with a reason, and the policy decides nothing again.
- **The plan's own policies.** The clinician's sign-off on the plan's own policies (overrides included) is their
  adoption; they need nothing more.
- **On by default** (since after v0.1.1; before, library policies decided as published unless `-require-adoption` was
  given). `-require-adoption=false` lets them decide as published, for a demonstration or a trial, as the public demo
  does; per plan, `"requireAdoption": false` or `true` in a `-plans` file overrides the flag, with
  `"adoptionSignatures"` beside it.

## When the program brings a new version

The library is part of the program, so a new Vaglio release can carry a new version of a library policy.

- Every adopted text is kept in `DATA/adopted`.
- With adoption required, the version the plan adopted goes on deciding while the new one waits. The policy's screen
  says so, and the review packet shows the lines that changed.
- Once enough clinicians adopt the new version, it decides instead, at once.

## When Medicare changes the source

`serve -watch-sources` asks the CMS Coverage API once a day for the current version of each document the library was
drafted from. It reads two public reports (every final LCD, every NCD) and sends nothing about the plan.

- A policy whose source has a newer version, or is to be retired, or is no longer listed, is flagged on its screen,
  in `GET /api/adoption`, and in the server's log.
- The flag changes nothing by itself: the policy decides as adopted until a clinician acts.
- `vaglio sources` does the same check once, from the command line. It exits 1 when a source has changed.
- One library copy (the PAP devices LCD, L33718) does not say which version it was copied from. It is reported as
  unknown: only a person comparing the texts can tell.

## Periodic review (`-review-every-days`, 365 by default)

UM accreditation (URAC, NCQA) expects criteria to be reviewed by clinicians at least yearly.

- Every policy in force, the plan's and the library's, is due for review that many days after its last adoption,
  review or sign-off. `0` turns the schedule off.
- A review is recorded like an adoption, with an outcome: kept as it is, or needs changing. A "needs changing" review
  must say what needs changing.
- The policy's screen and `GET /api/adoption` warn 30 days before a review falls due. The report counts the reviews
  that are overdue.

## The review packet

`GET /api/policies/{id}/packet`, and the policy's screen, give what a clinician reads before acting:

- the policy's text, and the source document beside it (for library policies, which carry a copy of their source);
- each clause of the policy, and whether it is in the source word for word;
- the policy's warnings, and its exhaustive check over every combination of evidence;
- the services it decides;
- the lines added and removed since the version last adopted.

## What code cannot do

The clinicians themselves cannot be supplied in code: they are the plan's own, or a clinical advisory panel BioDream
would need to hire. Vaglio records who approved what and when. It does not make anyone's approval a good one.

## What is tested

- `internal/adoption` and `internal/server/adoption_test.go`:
  - no decision until two clinicians adopt;
  - a non-clinician, a second adoption by the same clinician and a stale text are all refused;
  - a review falling due and recorded, and a withdrawal;
  - an older adopted version deciding while a new one waits, and the new one taking over once adopted.
- `internal/cmswatch`, against a stand-in of the two reports. `vaglio sources` was also run against the real Coverage
  API on 2026-10-24: all 10 sources unchanged (L33718's copy, which stated no version, was checked line by line
  against version 52 and now records it).
