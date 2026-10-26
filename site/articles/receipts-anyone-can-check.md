---
title: Receipts anyone can check, and a log that shows if it was changed
short: Receipts anyone can check
tile: Every decision gets a short receipt code. The log behind it shows any later edit, and can be witnessed by public services outside the plan.
area: Oversight
icon: receipt
order: 6
description: How Vaglio records every decision so that a change made afterwards shows, how a patient, provider or regulator can check a decision with a short code, and what outside witnesses add.
---
A decision is only as trustworthy as the record of it. If a plan can change what it decided after the fact, quietly editing a denial reason before an appeal, or removing an awkward case before an audit, then every other safeguard is weaker than it looks. Vaglio is built so that its record of every decision can be checked, by the plan itself and by people outside it, and so that a later change would show. This article explains how, in plain words, and is careful about what it does not prove.

## The log

Everything Vaglio does that matters is written to one file, its audit log: every decision with its proof, every review by a person, every override, every appeal, every policy put in force, every sign-in and every reading of a case. Nothing is ever deleted or rewritten in it; new entries are only added to the end.

Each entry carries a **fingerprint** of the entry before it. A fingerprint here is a SHA-256 hash: a 64-character code computed from the exact contents of the entry. Change one letter of an entry and its fingerprint changes completely. Because each entry contains the fingerprint of the one before, the entries form a chain. Changing, removing or reordering any entry breaks the chain from that point on.

Vaglio checks the chain every time it starts, and will not start on a log that has been tampered with. Anyone with the data can run the same check:

```
$ vaglio audit verify -data data
```

It walks the whole chain and prints the fingerprint of the last entry. Keeping that fingerprint somewhere else, in an email or a ticket, means that the log up to that point can always be checked against it later.

The log is written before an answer is sent. If Vaglio cannot record a decision safely on disk, it does not give the decision. We test this by killing the server with no warning while it is under load, and by replaying simulated power cuts at hundreds of points; no decision that was answered has ever been lost.

## The receipt

Every decision gets a **receipt**: a short code such as `VG-7K2D-Q9XM-4HTB-R3WC`. The code is the beginning of that decision entry's fingerprint, written in a form people can read aloud and type.

The receipt goes back with the answer to the provider, is printed on the letter to the patient, and is shown on the case. Anyone who holds it can check it on the plan's public receipt page, without signing in, and without seeing anything about the patient. The page shows:

- the decision (approved, denied, pended), when it was made, and by whom: the engine, or a named reviewer with their credential;
- the policy, its version, and the clause the decision rests on;
- anything that happened to it later, such as a review that reversed it or an appeal;
- where the entry sits in the log, and whether the log, checked from the start, still holds it unchanged.

![The public receipt page: a decision checked against the log, with the policy and clause it rests on.](/img/receipt.png)

The page shows no name, no diagnosis and no date of birth. A receipt proves that a particular decision exists in the log and has not been changed, not who it was about. The page also limits how many receipts one address can look up per minute, so the codes cannot be guessed in bulk.

So a patient told "your request was denied" can check that the denial on their letter is the one the plan recorded. A provider preparing an appeal can show the clause and the policy version in force that day. A regulator sampling decisions can confirm that what the plan shows them is what the plan decided.

## The weak spot, and how witnesses close it

The chain has one honest weakness. The plan holds the whole file. Someone with full access could rewrite every entry from some point onward and recompute every fingerprint after it. The new chain would check out perfectly, because nobody outside the plan saw the old one.

The only cure is someone outside the plan who saw the log earlier. Vaglio calls this **anchoring**, and it works in three parts.

### Signed checkpoints

At an interval the plan chooses, every hour for example, the server writes a **checkpoint**: how many entries the log holds, a single fingerprint that covers all of them, and the time. It signs the checkpoint with its own key, so anyone with the plan's published key can tell the checkpoint is genuine.

The fingerprint that covers all entries is built as a Merkle tree, the same structure the web's Certificate Transparency logs use. That structure gives two useful proofs:

- an **inclusion proof** shows that one particular entry is covered by a checkpoint, using only a handful of fingerprints and nothing else from the log;
- a **consistency proof** shows that a later checkpoint extends an earlier one, meaning it contains every entry the earlier one had, unchanged, plus new ones.

Each new checkpoint must extend the previous one. On every start, the server checks that the log still contains everything it has signed. If it does not, the server says so, stops signing checkpoints, and every receipt page shows the problem in red.

### Outside witnesses

Each checkpoint can be sent to witnesses the plan chooses. None is on by default, because each sends something off the plan's machines:

- **OpenTimestamps**, a free public service that records fingerprints in the Bitcoin blockchain within a few hours. Nobody, including the plan and BioDream, can later change what was recorded there.
- **A time-stamping authority** under the RFC 3161 standard, such as DigiCert or FreeTSA, which signs "this fingerprint existed at this time".
- **Sigstore's Rekor**, a public transparency log run for software supply-chain security, which anyone can search.
- **An endpoint a regulator or partner runs**, which simply receives and keeps every signed checkpoint.

Once a witness has a checkpoint, rewriting the log before it would contradict the witness. The plan cannot recall what Bitcoin, a time-stamping authority or a regulator has already received.

### Only fingerprints leave

Nothing about patients leaves the plan. A witness receives only a fingerprint of a checkpoint. And because a fingerprint of a small record could in principle be matched by guessing its contents, each entry is first mixed with a secret random value before it goes into the tree, so a fingerprint cannot be traced back to a patient by trying names and dates.

### Checking it all, offline

A receipt's answer carries everything needed: the entry's inclusion proof, the signed checkpoint, and what the witnesses answered. Whoever holds it can check it on their own computer, with the plan's published key, and without trusting the plan's server:

```
$ vaglio anchor verify -key PLAN-KEY receipt.json
```

The console's receipt page shows "Anchored", with the time and the witness.

## What this proves, and what it does not

We want to be exact here, because claims about tamper-proof records are often inflated.

It **proves** that the entries covered by a witnessed checkpoint have not been changed, removed or reordered since the time the witness recorded it. It proves which decision a receipt stands for, which policy version it applied, and who made it.

It **does not prove** that a decision was right. A wrong decision, faithfully recorded, is still wrong. It also does not prove that nothing was left out of the log before it was first anchored: a request that was never recorded leaves no fingerprint to check. And it does not stop someone with full access from deleting the whole log; it makes that visible, because the receipts and the witnesses still exist.

That is why the receipt sits beside the other safeguards rather than replacing them: proofs that can be read, clinicians who sign off policies and make denials, and reports that show patterns across many decisions.

Next: how [clinicians stay in charge](/learn/clinicians-stay-in-charge/) of every policy and every denial.
