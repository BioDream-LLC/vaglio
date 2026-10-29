---
title: Every situation, checked before a policy decides anything
short: Every situation, checked
tile: Each policy is tried on every combination of evidence it could ever be given. One that would contradict itself, or leave a gap, is refused.
area: Deciding
icon: grid
order: 4
description: Why testing a coverage policy on a few example patients is not enough, how Vaglio tries every possible situation instead, and the other checks a policy passes before it may decide a real request.
---
A coverage policy is a set of conditions that interact. Each condition is simple on its own; together they can surprise their authors. Two rules can both apply and point in opposite directions. A combination nobody thought of can fall through every rule. An exception written for one case can quietly cancel another. Vaglio does not trust a policy because it handles the examples its author tried. Before a policy may decide a single real request, Vaglio tries it on **every** combination of evidence it could ever be given.

## Why a few test cases are not enough

Suppose a policy has 10 pieces of evidence: red flags, therapy tried, prior imaging, symptoms, age, and so on. Each can be present or absent. The number of different records the policy might meet is 2 multiplied by itself 10 times: 1,024. With 14 pieces of evidence it is 16,384.

A careful author might write 20 or 30 test patients. Those will be the obvious ones: the clear approval, the clear denial, the one tricky case they remember from last year. The problems hide in the other thousands: the patient with a red flag *and* a recent scan, the one with surgery planned *but* no conservative therapy, the one where two exceptions overlap.

A computer does not get bored. Vaglio solves the policy on every one of those combinations and looks at each answer. For a policy of this size, that takes well under a second.

## What the check looks for

For every combination of evidence, Vaglio works out what the policy would decide on a record that documented exactly that. It refuses the policy if any of these happen:

- **It contradicts itself.** On some combination, the policy would both approve and deny. That is never a reviewer's judgment call: it is a mistake in the policy, and it must be fixed before the policy is used.
- **It rejects everything.** On some combination, every possible answer is overruled, so the policy gives no answer at all.
- **A decision can never be reached.** The policy declares an answer, such as "deny: duplicate study", that no combination of evidence ever produces. That usually means a rule is written wrongly, because the author meant it to apply somewhere.

When the check fails, it says on which combinations, so the author can see the situation that breaks the policy and fix it. When it passes, it reports how the combinations split: for the illustrative lumbar MRI policy, 32 combinations of its five pieces of evidence, 25 approved and 7 denied.

```
$ vaglio check policies/lumbar_mri.vaglio
PASS  lumbar_mri  (32 combinations of evidence)
      approved/decided                   25
      denied/decided                     7
```

## The absence rule, checked as well

The check above looks at fully documented records. But real records leave things out, and Vaglio's central rule is that [missing evidence never denies](/learn/missing-evidence-never-denies/). That rule is enforced at the moment each request is decided: before any denial is given, the engine confirms that no evidence the record leaves unaddressed could change it. For the example MRI policy, a test tries all 243 records where each of the five pieces of evidence is present, absent or not addressed, and confirms that every denial passes that test.

## Warnings, for things that are legal but probably wrong

Some things are allowed but are usually a mistake. Vaglio's checker lists them as warnings for the author, while still letting the policy load. Examples:

- a piece of evidence, or a criterion, that overrules nothing, so it can never change a decision;
- a criterion that nothing overrules, so it always holds;
- a piece of evidence with no way to find it in a request, so only a person can ever supply it;
- one questionnaire item read as two different pieces of evidence.

Separately, a policy drafted from a document must quote every clause word for word from that document, or it is refused outright (see [the next article](/learn/ai-drafts-a-person-approves/)).

## Seeing the whole policy at once

The checker's results are also drawn. On the console, every policy has a **policy map**: a grid of every combination of its evidence, each square coloured by the decision it gets. A clinician reviewing a policy can see at a glance where the approvals are, where the denials are, and which piece of evidence moves a large area from one to the other. Clicking a square shows that combination of evidence, the decision it gets and the clause that decision rests on. Beside the grid, a diagram draws the policy itself: every rule as a box, and an arrow from each rule to the rules it overrules.

![The policy map: every combination of the policy's evidence, coloured by the decision each one gets.](/img/map.png)

## Replayed over past cases, before it goes live

A new version of a policy can pass every check and still change outcomes in ways the plan did not intend. So before a new version is put in force, Vaglio can **replay** it over every past case in the log and show which decisions would have changed: which approvals would have become denials, which pends would now be decided, and why, with the two proofs side by side.

This also works the other way. Every version of a policy that has ever been in force is kept. A slider on a case lets a reviewer see how that case would have been decided under each version, which helps in appeals and audits: "under the policy in force on the day, this was the right answer; under today's, it would be different."

## Two readings of the same sentence

The checks so far prove that a policy is *consistent*. They cannot prove it says what the source document *means*. Coverage documents are written in English, and English can be read more than one way.

Vaglio has a tool for that, too. Two people (or a person and a model) each write a policy from the same document, both quoting it word for word, both passing every check. Then `vaglio readings` compares them on every combination of evidence and lists every record the two would decide differently, with the simplest such record first. Where two faithful readings disagree, the document admits both. That is a possible ambiguity, and only a clinician who knows the intent can settle it.

We ran this on public Medicare documents and published what it found. In one knee-replacement document, two readings disagreed on 630 of 16,384 possible records (3.8%). In one sleep-apnea document, a reading that lost the indentation of the original web page disagreed with the careful one on 512 of 4,096 records (12.5%). [The Medicare library article](/learn/medicare-rules-checked/) tells that story.

## What the checks do not prove

It is worth being precise about the limits:

- **Consistency is not correctness.** A policy can pass every check and still be medically wrong, or misread its source. That is why a clinician must sign off every policy before it decides anything, and why the plan's clinicians must adopt each library policy. See [clinicians stay in charge](/learn/clinicians-stay-in-charge/).
- **The check covers the policy, not the record.** It assumes the evidence was found correctly. Finding it is tested separately, line by line, against sample requests.
- **Size has a limit.** A policy may hold up to 40 pieces of evidence. Up to 16 (65,536 combinations), every combination is solved one by one. Past that, the check walks the evidence one piece at a time and stops branching as soon as the outcome no longer depends on what is left, so a list of 30 alternative indications, any one of which is enough, is checked exactly in a fraction of a second. The counts are the same as solving every combination; we test that on every library policy and on hundreds of random ones. A policy too tangled to finish this way fails its check and says so. In our study of 60 Medicare documents, none needed more than 16 once alternatives were written as one piece of evidence.

## Why it matters

A plan that puts a policy into software is making a promise: every request for that service will be decided by these rules. Checking every situation in advance turns that promise into something that can be shown. When a regulator asks "could your system have denied a patient in this situation?", the plan can answer from the map, not from memory. And when a policy is wrong, the problem is found by a computer before a patient meets it.

Next: how a model can draft a policy from a coverage document, and why [a person approves every one](/learn/ai-drafts-a-person-approves/).
