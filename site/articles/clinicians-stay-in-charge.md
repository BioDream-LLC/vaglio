---
title: Clinicians stay in charge of every policy and every denial
short: Clinicians stay in charge
tile: A clinician signs off every policy before it decides. A plan's own clinicians adopt the library. A plan can require that only a clinician ever denies.
area: Oversight
icon: person
order: 7
description: Where people make the decisions in Vaglio and the software only helps, from approving the rules it runs on, to denials, peer-to-peer calls and appeals, and why each safeguard exists.
---
Software can apply a rule faster and more consistently than any person. It cannot decide what the rule should be, and it should not be the last word on whether a patient goes without care. Vaglio is built around that division of labour. People decide what the rules are and make the hard calls; the engine applies the rules exactly, shows its work, and hands people everything the rules cannot settle. This article walks through each place a person is in charge, and why.

## Who the people are

Vaglio has a few kinds of account, each allowed only what its work needs:

- **Viewers** can read cases and policies.
- **Reviewers** can also decide pended cases.
- **Clinicians** are reviewers whose account records a national provider number (NPI) and a credential, such as "MD, radiology". Some acts only a clinician can do.
- **Administrators** can also read the audit log and manage accounts.
- **Providers** can sign in to the plan's provider portal to see their own requests and send what is missing, and nothing else.

Every act of every account is in the [audit log](/learn/receipts-anyone-can-check/), under that account's name.

## Before anything decides: the rules are approved by clinicians

### The plan's own policies

A policy a plan writes, in the console's editor or from a draft a model produced, decides nothing until a clinician **signs it off**. The sign-off records the clinician's name, NPI and credential, the date, and a fingerprint of the exact text they approved. If the text changes by one character afterwards, it is a new version, and it needs a new sign-off.

### The built-in library

Vaglio comes with a library of policies written from public Medicare documents: knee replacement, CPAP for sleep apnea, power wheelchairs, spinal injections, varicose vein treatment and others. Each quotes its source word for word and passes every check. But nobody outside BioDream has reviewed them clinically, and the rules plans work under expect their own clinicians to approve the criteria they apply:

- For Medicare Advantage plans, 42 CFR 422.137 requires a utilization management committee that reviews the plan's utilization management policies at least once a year.
- NCQA, which accredits health plans, expects practising clinicians to be involved in adopting and reviewing the criteria a plan uses.

So since version 0.2.0, **a library policy decides nothing for a plan until that plan's clinicians adopt it.** Adoption works like this:

- A clinician opens the policy's **review packet** in the console. It shows the source document beside each clause, the full check, the [policy map](/learn/every-situation-checked/), the codes, and what changed since the version last adopted.
- They adopt the exact text, recorded with their name, NPI, credential, their role on the committee, and the committee's reference, such as the minutes of the meeting.
- A plan can require more than one signature: for example, a specialist in the field and the medical director, counted as two different NPIs.
- If the text changed after the clinician opened it, the adoption is refused. Nobody can adopt a version they did not read.
- A clinician can **withdraw** an adoption, with a reason, and the policy stops deciding at once.

Until a library policy is adopted, its requests are answered "decides nothing", which means the plan's prior authorization server handles them exactly as it would with no Vaglio at all, usually by sending them to the plan's reviewers.

A plan that just wants to try Vaglio out can turn this off, and the public demonstration does. But the default is the safe one.

### Keeping approvals current

Medicare updates its coverage documents. When a newer version of a library policy arrives in a new Vaglio release, it does not take over by itself: it waits for adoption, while the version the clinicians already adopted goes on deciding. Vaglio can also ask Medicare's Coverage API whether any source document has a newer version, and say which.

And every policy in force falls due for a clinician's review at an interval the plan sets, yearly by default. Vaglio warns 30 days before a review is due and reports which policies are overdue, so the annual review that accreditation expects cannot quietly lapse.

### Where a plan reads a rule differently

If a plan's medical director reads a library rule differently, they **override** it: the console starts a draft of the next version for the plan, and once a clinician signs it off, the plan's version decides instead. Removing the override, also a clinician's act, puts the published one back. The policy list says plainly which library policies are as published and which are overridden.

## Denials: optionally, only a clinician

By default, the engine denies on its own only when the record *documents* that the policy is not met, never because something is missing. Many plans and several states want more than that. California's SB 1120, for example, requires that a licensed physician or other qualified clinician make medical necessity decisions, and more than a dozen states now require a clinician to make or review a denial when software was involved.

With one setting, Vaglio **never denies on its own**. Where it would deny, it *proposes* a denial instead, with the full proof, and puts the case in front of a clinician. Only an account recorded as a clinician can make the denial, and their NPI and credential go into the log beside it. The setting is made where the server is started, not in the console: who may deny care is the plan's decision, not something a user can switch.

## The reviewers' day

Whatever the rules cannot settle comes to people in a **work queue**:

- **Cases arrive with the work done.** Each shows the evidence found and where, what is missing, the proposed answer if there is one, the proof, and the due date.
- **The queue is ordered by stated rules**, such as urgency and how close a case is to its deadline. The rules say only which case comes first; they never decide a case.
- **Cases can be assigned**, or a reviewer can simply take the next one.
- **A physician tier** keeps the cases that need a physician away from other reviewers.
- **Peer-to-peer calls**, where the provider's doctor speaks with the plan's, are requested and recorded on the case.

![The work queue: cases ordered by stated rules, each with its due date and the reason for its place.](/img/queue.png)

Every review is recorded with who made it and why, and if a reviewer's decision differs from what the engine proposed, that is recorded too. Those differences are among the most useful data a plan has; the [reports](/learn/reports-regulators-ask-for/) show them by clause, by provider and by region.

## Appeals

When a provider or patient appeals a denial, the appeal is taken into Vaglio and decided there, under the rules for appeals:

- **Deadlines**: 30 days for a standard appeal, 7 days for a drug, and 72 hours for an expedited one. The due date is on the case.
- **Not by the person who denied it.** The account, and the NPI, that made the denial cannot decide its appeal.
- **Upheld only by a clinician.** A reviewer who is not a clinician can overturn a denial on appeal, but only a clinician can uphold one.

Each appeal's outcome is recorded against the original decision, so the receipt for that decision shows it, and the reports show how often denials are overturned, policy by policy and clause by clause.

## If the plan already has a case-review system

Most plans already run a utilization management system where their nurses and physicians work cases. Vaglio does not ask them to move. It can open a case in that system for every request it pends or proposes to deny, with the proof, the missing evidence and a link back, and take the reviewer's decision from it when it is made. That system stays the record of the human decision; Vaglio never closes a case there on its own. [How it fits a plan's systems](/learn/fits-the-plans-systems/) explains the connection.

## Why the safeguards are built in, not bolted on

Every one of these protections can be described as a policy in a handbook. The difference is that in Vaglio the software enforces them: a library policy cannot decide without its adoption record; a denial cannot be made under the denier's NPI on appeal; a proposed denial cannot be finalised by a non-clinician. When a regulator or a court asks how a plan ensures clinicians were in charge, the answer is not "our procedure says so", but the log, entry by entry.

Next: [the reports](/learn/reports-regulators-ask-for/) that a plan must publish, and the questions it should ask itself.
