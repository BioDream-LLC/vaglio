---
title: Missing evidence never denies care
short: Missing evidence never denies
tile: Silence in a record is not a "no". When a gap could change the answer, Vaglio pends the request and asks for exactly what is missing.
area: Deciding
icon: search
order: 3
description: The one rule Vaglio puts above all others, why it matters so much in prior authorization, and how the engine works out exactly which missing evidence to ask for.
---
Vaglio has one rule that outranks every other: **a denial may rest on evidence of absence, never on absence of evidence.** It sounds like a play on words. It is the difference between "the record shows the patient did not have physical therapy" and "the record does not mention physical therapy". The first can justify a denial. The second never does. This article explains why that distinction matters so much, and how Vaglio applies it to every request.

## Two kinds of "not there"

Imagine a policy that covers a knee replacement after the patient has tried physical therapy without relief. Three different records could arrive:

1. The record includes six weeks of physical therapy notes and says the pain did not improve. The evidence is **present**.
2. The record says the patient has not tried physical therapy, or the provider's questionnaire answers "no". The evidence is **absent**, and the record says so.
3. The record says nothing about physical therapy at all. The evidence is **not addressed**.

Many systems, and many tired human reviewers, treat the third case like the second: "I don't see physical therapy, so it wasn't done, so deny." That is the mistake Vaglio is built to refuse. A record can be silent for many reasons that have nothing to do with the patient:

- the therapy happened at a different clinic, whose notes never reached the provider's system;
- the provider's software sent only part of the chart;
- the request came by fax, and a page was missing;
- the person filling in the form did not know the plan cared about it;
- the record uses a code the plan's policy does not list, so the evidence was there but not recognized.

In each of those cases, a denial is wrong. It is also expensive for everyone: the provider resubmits or appeals, the patient waits, and the plan does the work twice. When denials are appealed, a large share are overturned, and missing paperwork is one of the most common reasons.

## What Vaglio does instead

When a piece of evidence is not addressed, Vaglio does not guess either way. It asks a precise question: **could this evidence, if present, or if absent, change the answer?**

- If it could not, the evidence does not matter for this request, and the answer is given without it. For example, once a red flag is found, the policy covers the MRI whatever the conservative-therapy history is, so Vaglio does not ask about therapy.
- If it could, the request is **pended**, and that evidence is asked for by name.

The engine works this out exactly, not by rule of thumb. It knows every argument in the policy and what defeats what, so it can tell, for each unaddressed piece of evidence, whether either answer would tip the outcome. Only the evidence that matters is requested. A provider is never asked for ten documents when one would settle it.

Here is how it looks for the MRI example used across these articles:

- Present: six weeks of failed conservative therapy.
- Not addressed: whether there was a lumbar MRI in the last six months.

If there was no recent MRI, the request would be approved. If there was one and nothing has changed, the request repeats a recent study and would not be approved. The question decides it, so Vaglio pends and asks: *"Was a lumbar spine MRI performed in the last six months?"*, and, because it would matter in that case, *"Are there new or worsening symptoms since then?"*

## Asking in a form the provider can answer

A pend is only useful if the provider can answer it quickly. Vaglio makes the request specific:

- **Each missing item is named in the policy's own words**, with the clause it comes from.
- **It is tied to a question.** Vaglio builds the plan's questionnaire for each service from the same policy file. This is the Da Vinci DTR questionnaire, which a provider's system can show to the clinician and partly fill in from the chart by itself. Because the questions and the decision come from one source, a question can never ask for something the decision does not use, and the decision can never need something no question asks.
- **It names documents, too**, where the policy lists the kind of record that would show the evidence, such as a radiology report or therapy notes, using the standard codes for document types.
- **The letter says the same thing.** Vaglio writes the pend letter from the proof by template, in English or Spanish, listing exactly what is needed and by when.

The plan can also answer *before* a request is sent. Through Perfuse, the provider's system can ask "would this be approved as the record stands?" while the doctor is still placing the order. Vaglio answers "would be approved", "would wait for these pieces of evidence", or "would not be approved, and here is what would change that". It opens no case and decides nothing; the request that follows is decided on its own. But the missing evidence can be added before the request ever leaves the office.

## When is a denial allowed?

Only when the record *documents* that the policy is not met, and nothing the record leaves open could rescue the request. In the knee example, a denial for lack of therapy is possible only in case 2, where the record says therapy was not tried, and only if nothing else in the policy would cover the surgery anyway.

Even then, a plan can go further. With one setting, the engine never denies on its own: it *proposes* a denial, and only a clinician can make it, with their NPI (their national provider number) and credentials recorded. Several states now require exactly that for denials made with the help of software, and some plans want it everywhere.

## Contradictions go to a person

Sometimes a record says both things: one note says therapy was completed, another says it was never started. Vaglio does not pick the convenient one. It marks the evidence as contradicted, treats it as not settled, and if the answer depends on it, sends the case to a clinician. It does not ask the provider to resolve it by default: a contradiction inside a record is a clinical question, not a missing form.

## How we know the rule holds

A rule like this is easy to claim and easy to break by accident, for example with a policy whose conditions interact in a way nobody noticed. So it is tested in three ways:

1. **By brute force.** For the example MRI policy, every possible request was tried: each of its five pieces of evidence present, absent or not addressed, which is 3 × 3 × 3 × 3 × 3 = 243 requests. For every one where the engine denied, the test checks that no unaddressed evidence could have changed the answer.
2. **On every policy, before it decides anything.** Vaglio's checker runs each policy through every combination of evidence it can be given and refuses a policy that could deny where it should pend. See [every situation, checked](/learn/every-situation-checked/).
3. **In the reports.** Vaglio reports which missing evidence causes the most pends, policy by policy. A plan that sees one question causing most of its pends can fix the questionnaire, or ask its providers to send that record by default.

## The deadline does not stop

A pend is not a way to delay. Under CMS-0057-F, the clock for the plan's answer keeps running: 7 days for a standard request and 72 hours for an urgent one. Vaglio keeps the due date on every case, in front of the reviewer, and puts the cases closest to their deadline first in the work queue. Where the rules allow an extension (Medicare Advantage and Medicaid allow up to 14 days in some cases), it records the extension, the reason and the new date, and the letter says so. If the provider never answers, the case does not quietly turn into a denial. It stays with a person, who decides it before the deadline.

## Why this matters beyond fairness

Treating silence as "no" is unfair to patients, and it also makes a plan's decisions fragile. A denial built on a gap will often be overturned on appeal, and it can be hard to defend to a regulator or a court. A denial built on what the record documents, with the clause and the record line shown, is one the plan can stand behind. And a pend that names exactly what is needed is usually answered quickly, which is better for the patient, the provider and the plan.

Next: how Vaglio [checks every situation a policy could face](/learn/every-situation-checked/) before it is allowed to decide a single request.
