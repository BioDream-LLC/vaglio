---
title: How Vaglio decides, and why every answer is a proof
short: How Vaglio decides
tile: Policies written as short rules in the plan's own words. Evidence found in the record, with where it came from. An answer that shows every step.
area: Deciding
icon: tree
order: 2
description: A step-by-step walk through one request, from the moment it arrives to the answer it gets, and what makes that answer something a reviewer, a court or a regulator can read and check.
---
Most software that decides prior authorization requests gives an answer and, at best, a summary of why. Vaglio gives an answer and the whole argument behind it, step by step, each step tied to a sentence of the plan's policy and to a place in the patient's record. This article follows one request through the engine and explains each stage in plain words.

## The policy, written down as rules

Everything starts with the plan's coverage policy, the document that says when a service is covered. In Vaglio, each policy is turned into a short text file of rules. The file keeps the plan's own words: every rule carries the exact sentence of the policy it stands for, quoted word for word.

A policy file has three kinds of rule:

- **Evidence**: a fact the policy asks about. For a lumbar spine MRI, "at least six weeks of conservative therapy without improvement" is a piece of evidence. Each one says where in a request it can be found.
- **Criteria**: the policy's conditions, such as "no indication for the MRI has been established", or "the request repeats a recent study".
- **Decisions**: the possible answers, such as "approve" and "deny: none of the indications is met".

What makes the file work is one phrase: **defeated by**. Each criterion and each decision lists what overrules it. Here is part of the illustrative MRI policy that ships with Vaglio:

```
criterion indication_unestablished
  text "No indication for MRI has been established."
  clause "MRI of the lumbar spine is covered when one of the indications in this section is met."
  defeated by red_flag conservative_therapy_6wk surgical_planning

decision approve approves
  text "Approve MRI of the lumbar spine."
  clause "MRI of the lumbar spine is covered when one of the indications in this section is met."
  defeated by indication_unestablished duplicate_study
```

Read it as a conversation. "Approve" stands unless someone shows that no indication has been established, or that the request duplicates a recent study. "No indication established" stands unless a red flag, six weeks of failed conservative care, or a planned surgery is found. Each block reads like the clause it comes from, together with everything that overrides it.

This style of reasoning has a name in computer science, *argumentation*: arguments that attack each other, and a precise rule for which ones survive. Vaglio uses a well-studied version of it, called grounded semantics, which accepts only what can be defended without any guessing. You do not need the theory to read a policy. What matters is that the rule for "which arguments win" is fixed, mathematical and the same for every request, so two people applying the same policy to the same record always get the same answer.

## Step 1: the request arrives

A provider's system sends a prior authorization request. In the standard way, that is a Da Vinci PAS request, a bundle of FHIR records: the service asked for (for example CPT code 72148), the patient, the diagnoses, past procedures, test results, medications, and often the answers to the plan's questionnaire. It arrives at the plan's PAS server, such as Perfuse, which passes it to Vaglio.

A request can also come in by fax, by phone or through a provider portal. A person keys it into Vaglio's intake form, and from then on it takes exactly the same path.

Vaglio finds which policy covers the service code. If several do, for example a national Medicare rule and a plan's own policy, a fixed order decides which one applies. That order depends on the dates each policy is in force, the line of business (Medicare Advantage, Medicaid, commercial), the patient's state, and the authority behind the policy. If no policy covers the service, Vaglio says so and decides nothing, and the PAS server handles the request as it would without Vaglio.

## Step 2: the evidence is found, with where it came from

For every piece of evidence the policy names, Vaglio looks through the request in the places the policy says to look. Each "find" line in the policy is one way of finding it:

- an answer to the plan's questionnaire;
- a diagnosis with particular codes, perhaps only when confirmed rather than suspected;
- a procedure in the last six months;
- a test result above a threshold, such as a body mass index of at least 40;
- a medication taken for at least 90 days;
- a result computed by the plan's own CQL logic, the standard language for clinical rules;
- the plan's licensed criteria, asked through the plan's own criteria service.

Each piece of evidence ends up in one of three states:

- **Present.** The record shows it. Vaglio notes exactly where: which record, which code, which questionnaire answer.
- **Absent.** The record shows it is *not* there. For example, the questionnaire answers "no", or the record shows the therapy was not done. This is called *evidence of absence*.
- **Not addressed.** The record says nothing either way.

The difference between "absent" and "not addressed" is the most important idea in Vaglio, and it has [an article of its own](/learn/missing-evidence-never-denies/). In short: silence is never treated as a "no".

If the record says both yes and no about the same evidence, Vaglio does not pick one. It marks the evidence as contradicted, and if the answer depends on it, the case goes to a clinician.

## Step 3: the policy is applied

With the evidence in hand, the engine works out which arguments survive. In the MRI example:

- six weeks of failed conservative therapy is present, so "no indication established" is defeated;
- the record does not address whether there was an MRI in the last six months.

Could "approve" still be defeated? It depends on the MRI question. If there was a recent MRI and nothing has changed, the request repeats a recent study and approval is overruled. If there was none, approval stands. The record does not say, so the engine cannot settle it, and it will not guess.

Vaglio checks this exactly. For each piece of evidence that is not addressed, it asks whether its being present, or its being absent, could change the answer. If it could, the request is **pended**, and the missing evidence is named, together with the questionnaire item that asks for it. If no unaddressed evidence could change the answer, the answer is final.

The engine reaches one of these outcomes:

- **Approve**, when the policy's approval stands and nothing the record leaves open could defeat it.
- **Deny**, only when the record *documents* that the policy is not met, and nothing it leaves open could rescue the request.
- **Pend for evidence**, when something the provider can supply would settle it.
- **Send to a person**, when the record contradicts itself, when the policy cannot settle the case, or when the plan has said that only a clinician may deny.

## Step 4: the proof

Whatever the outcome, Vaglio writes down why, as a tree of steps. Each step says:

- what was concluded ("an indication for MRI is established");
- the clause of the policy behind it, quoted word for word;
- the evidence it rests on, and the place in the record each piece came from;
- for a pend, what is missing and what would follow either way.

On the case screen the proof appears as a tree a reviewer can open step by step, with the deciding step highlighted. For a denial, every step can be followed back to a sentence of the policy and a line of the record. Nothing in it is a model's impression or a score.

![A pended case: the evidence found, with its source, and the evidence the record leaves unaddressed, asked for by name.](/img/pend.png)

## Step 5: recorded before it is sent

Before Vaglio answers, it writes the decision, the proof and the request itself into its audit log and makes sure they are on disk. If it cannot record a decision, it does not give it. The log is built so that any later change shows: each entry contains a fingerprint of the one before. Each decision also gets a short receipt code that anyone holding it can check against the log later. [Receipts](/learn/receipts-anyone-can-check/) have their own article.

The answer then goes back to the PAS server, which sends it on to the provider in the standard form: approved, denied with the reason, or pended with what is needed.

## What would change it

Because the proof is exact, Vaglio can also answer "what would have changed this answer?" On any case, a reviewer can open the **what-if sandbox** and switch each piece of evidence between present, absent and not addressed. The decision and its proof redraw at once, and the step that decides is lit up. For a denial, Vaglio lists the smallest sets of evidence that would turn it into an approval, which is exactly what the provider needs to know.

![The what-if sandbox: one piece of evidence switched to present, and the denial becomes an approval, with its proof redrawn.](/img/whatif.png)

## Why this is worth the trouble

A model can be very accurate on average and still be unable to explain any single answer. A plan that has to give "a specific reason for every denial" under CMS-0057-F, defend a decision in an appeal or a lawsuit, or show a state regulator that a clinician reviewed it, needs the reason itself, not a description of it. Vaglio's answers are built from the reason up:

- **Same input, same answer.** The rules for which arguments survive are fixed. Nothing is random and nothing drifts.
- **Every step can be checked by hand.** A reviewer can read the policy sentence and the record line and see that the step follows.
- **The policy is tested before it decides anything.** Every combination of evidence a policy can be given is tried first; see [every situation, checked](/learn/every-situation-checked/).
- **It is fast.** Deciding with the full proof takes about 10 microseconds on one core of an Apple M4. Most of the time a whole request takes is spent writing the record safely to disk.

## What it does not do

The engine applies the policy it is given. It does not decide whether the policy itself is medically right; the plan's clinicians decide that, and Vaglio makes them sign off before a policy may decide anything. It does not trust free-text notes on a model's say-so: a document can be read by a model, but only as *proposed* evidence, each finding quoted word for word from the document. A reviewer confirms it, or, where the plan allows, it may approve a request on its own while people check a sample of those approvals. A finding read by a model can never cause a denial. And it does not replace the plan's reviewers; it hands them the cases the rules cannot settle, with the work already laid out.

Next: why [missing evidence never denies](/learn/missing-evidence-never-denies/), and what Vaglio does instead.
