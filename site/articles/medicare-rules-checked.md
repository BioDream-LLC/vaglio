---
title: "Medicare's own rules, written down and checked"
short: The library, from Medicare's rules
tile: Twelve policies written from public Medicare and state documents, each quoting its source. Checking them found places where careful readers can disagree.
area: Running it
icon: library
order: 9
description: What is in Vaglio's built-in policy library, where each policy comes from, why it cannot include the licensed criteria most plans use, and what turning public coverage documents into exact rules revealed about the documents themselves.
---
A decision engine is only as useful as the policies it runs. Most health plans decide prior authorization against licensed criteria from two companies, InterQual (owned by Optum) and MCG. Those criteria are proprietary; Vaglio does not include them and cannot share them. So Vaglio ships something different: a library of policies written from **public** coverage documents, mostly Medicare's, each one quoting its source word for word. This article explains what is in it, where it comes from, and what writing those documents down as exact rules taught us about the documents.

## What the library holds

Each policy in the library comes from one public document, kept beside it in the repository with where and when it was retrieved:

| Service | Source |
|---|---|
| CPAP for obstructive sleep apnea | Medicare LCD L33718 |
| Total knee replacement | Medicare LCD L36575 |
| Facet joint interventions for pain | Medicare LCD L38773 |
| Cardiac CT angiography | Medicare LCD L35121 |
| Epidural steroid injections for pain | Medicare LCD L39240 |
| Treatment of varicose veins | Medicare LCD L34536 |
| Blepharoplasty and eyelid surgery | Medicare LCD L34411 |
| Power mobility devices | Medicare LCD L33789 |
| Bariatric surgery for morbid obesity | Medicare NCD 100.1 |
| Hyperbaric oxygen therapy | Medicare NCD 20.29 |
| Adalimumab for rheumatoid arthritis | Utah Medicaid's published prior authorization form |
| Lumbar spine MRI | Washington State Labor & Industries' published imaging guideline |

**LCD** stands for Local Coverage Determination: a coverage rule written by one of the contractors that process Medicare claims in a region. **NCD** is a National Coverage Determination, which applies everywhere. Medicare Advantage plans must follow both where they apply. The procedure and device codes each policy covers come from the billing articles that go with each LCD, as code numbers only.

Each library policy says which authority it carries (national, local, state or plan), which lines of business it applies to, and which states. When more than one policy covers the same service for the same patient, Vaglio uses a fixed order: a national rule outranks a local one, which outranks a plan's own, and the dates each is in force are respected. Two policies of equal rank for the same service are refused, never silently both applied.

## How each one was written

Every clause in a library policy is a quotation: the rule carries the exact sentence of the source it stands for, and a test fails if the sentence is not found in the source word for word. Every policy passes [the full check](/learn/every-situation-checked/) on every combination of its evidence.

Where the source can be read two ways, the policy file says which reading it took, in a note at the top, for the plan's clinicians to confirm or change. The knee-replacement policy, for example, notes two such readings, and says which conditions it leaves to a human reviewer rather than encoding them.

Vaglio also keeps the library current. Each Medicare policy records the version of the document it was written from, and `vaglio sources` asks Medicare's public Coverage API whether a newer version exists. As of October 2026, all ten Medicare policies match the current versions.

What the library is **not**: it is not reviewed by any outside clinician, and it is not anyone's coverage policy until a plan adopts it. Since version 0.2.0, a library policy decides nothing for a plan until that plan's own clinicians adopt its exact text. See [clinicians stay in charge](/learn/clinicians-stay-in-charge/).

## Why not the licensed criteria?

InterQual and MCG criteria are what most commercial and Medicare Advantage plans use, and they are licensed products. Vaglio cannot include or redistribute them. A plan that licenses them is not left out, though. Its criteria can be run as a service the plan controls, and a Vaglio policy can ask that service a question, such as "is this criterion met for this request?". The proof then names the licensed criterion and what the service found. Vaglio's documentation includes a small example service to start from.

The standardized documentation lists that AHIP and BCBSA published in 2026, for 66 commonly reviewed services, are a different kind of thing: they set the most documentation a plan may ask for, not when a service is covered. AHIP's terms of use do not allow them to be copied or adapted, so Vaglio includes none of their text.

## What writing the rules down revealed

Turning a coverage document into exact rules forces every question the prose can leave open. Does "or" bind the last two conditions or all five? Does a condition listed under a heading apply to everything below it? A policy file must answer each one, and the checker will not let it skip any.

That led us to a test anyone can repeat. Write a second reading of the same document, just as faithful to its words and passing every check. Then let the computer find every patient record the two readings decide differently. Where two careful readings of the same sentence disagree, the sentence allows both. That is a possible ambiguity, or a mistake in one reading, and only someone who knows the clinical intent can say which. We published the results in [docs/medicare-readings.md](https://github.com/BioDream-LLC/vaglio/blob/main/docs/medicare-readings.md). Two examples follow. Nothing in them says Medicare, or any contractor, wrote anything wrong.

### Total knee replacement

The knee-replacement LCD lists the criteria for advanced joint disease (imaging, pain or disability, conservative therapy addressed). It then lists six findings joined by "or": failure of a previous osteotomy, distal femur fracture, malignancy, failure of a previous partial knee replacement, avascular necrosis, and proximal tibia fracture.

- **Reading A** treats each of the six as an indication on its own.
- **Reading B** treats each of the six as standing in for the imaging criterion only, so pain and the conservative-therapy history are still needed.

Both quote every clause word for word and pass every check. They decide 630 of the 16,384 possible records differently, 3.8%, always the same way: A approves, B denies. The simplest such record is a failed osteotomy and nothing else documented. The question for a clinician is plain: does a patient with a failed osteotomy, or a distal femur fracture, need documented pain and a conservative-therapy history before a total knee replacement is covered?

### CPAP for sleep apnea

The CPAP LCD's sleep-test criterion is either an apnea-hypopnea index (AHI) of 15 or more, *or* an AHI of 5 to 14 *and* documented symptoms or certain conditions. On the web page, the nesting is shown by indentation. In the plain text of the same page, the indentation is lost.

A model drafting from the plain text produced a policy that required the symptoms for *every* sleep test result. It quoted the rule word for word and passed every check. The library's reading requires them only for an AHI of 5 to 14. The two decide 512 of 4,096 records differently, 12.5%. The largest group, 352 records, is a patient with an AHI of 15 or more and no listed symptom: approved by the library's reading, denied by the model's.

This is less an ambiguity in the rule than a warning about reading it. A layout that a person reads at a glance is easy to lose, and a policy built from the lost version can be consistent, quote the rule exactly, and still be wrong. Comparing two readings by machine finds that. Checking either one alone does not.

## A study of 60 documents

We also drafted 60 Medicare LCDs, picked by a fixed rule rather than by us, to see whether the policy language fits real coverage text. With better feedback to the model, all 60 could be written as policies. But two models' readings of the same document agreed on every combination of evidence in only 2 of 37 comparable cases, and differed on a median of 3.9% of combinations. [AI drafts, a person approves](/learn/ai-drafts-a-person-approves/) tells that story in full. The lesson is the same as above: the exact form makes disagreements visible, and a clinician has to settle them.

## What this offers a plan

A plan using the library gets a starting point: policies already written in exact rules, tied to their sources, checked, and annotated where the source is unclear. Its clinicians still decide whether each reading is the one the plan will apply. A plan can also use the readings tool on its own policies: two staff members each write a policy from the same document, and the tool shows exactly where they disagree, before any patient meets the difference.

Next: [how Vaglio fits a plan's systems](/learn/fits-the-plans-systems/), from the standards it speaks to the fax machine.
