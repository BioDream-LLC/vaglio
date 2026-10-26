---
title: The reports regulators ask for, and the questions a plan should ask itself
short: Reports regulators ask for
tile: The CMS prior authorization numbers, gold carding, which services no longer need prior authorization, and whether errors fall harder on some patients.
area: Oversight
icon: chart
order: 8
description: The reports Vaglio builds from its own decision log, what each one measures, how it guards against misleading numbers, and how a plan can watch Vaglio on its own cases before trusting it with any.
---
A plan's prior authorization program is judged by its numbers: how fast it answers, how often it denies, how often those denials are overturned. Since 2026 many plans must publish some of those numbers. Vaglio builds its reports from its own audit log, the same record its decisions are kept in, so a report can never disagree with what was actually decided. This article goes through each report, what it is for, and the care taken to keep it honest.

## The CMS prior authorization metrics

The CMS Interoperability and Prior Authorization rule (CMS-0057-F) requires Medicare Advantage plans, Medicaid and CHIP programs, and marketplace plans to publish, each year, figures about their prior authorization decisions, starting with 2025's data in March 2026. In plain terms, for standard and urgent (expedited) requests separately:

- the share of requests approved and the share denied;
- the share approved after an appeal;
- the share where the decision deadline was extended, and then approved;
- the average and median time from request to decision.

Vaglio produces these from its log, on the Reports screen, from the command line, or as a spreadsheet file:

```
$ vaglio report metrics -data data -from 2026-01-01 -csv
```

One rule keeps the report honest: anything Vaglio did not see is reported as **not tracked**, never as zero. If appeals for some requests were handled entirely in another system, the report says it does not know their outcome, rather than implying none were overturned. Drug requests, which the rule leaves out of these figures, are counted apart.

## Gold carding

Several states, Texas first among them, now have **gold card** laws: a provider whose requests for a service were almost always approved over a recent period is exempt from prior authorization for it. The rules differ from state to state: the approval rate, the look-back period, the minimum number of requests, and sometimes which services.

Vaglio applies a plan's gold card programs exactly as they are written in a small settings file: the program's name, the law it comes from, the states, the approval rate, the look-back window and the minimum count. For each request, if the provider qualifies under a program, the request is approved without review, and the answer names the program and the provider's record that earned it. Two details matter:

- An approval granted *because of* a gold card never counts towards the next exemption. A provider's standing is earned by decisions on the merits.
- `vaglio goldcard` lists every provider's standing under each program, so the plan can see who qualifies, who is close and why.

Gold card law changes quickly, so the example numbers that ship with Vaglio are illustrative; a plan must check them against the law it applies.

## Which services still need prior authorization?

In 2025 the industry associations AHIP and BCBSA pledged that their member plans would require prior authorization for fewer services. The question every plan then faces is: *which ones?* A service whose requests are almost always approved, and whose denials are mostly reversed when reviewed, costs everyone time and protects nobody.

Vaglio's list review answers that from the log. For each service it shows:

- how often requests were approved in the end, whether by the engine, by a reviewer, through a gold card or on appeal;
- how many requests arrived;
- how often the engine's denials were reversed;
- the median time a person spent.

Each service then gets one of three verdicts, by thresholds the plan sets and the report states: **consider removing** prior authorization, **keep** it, or **not enough requests** to say. By default that means at least 30 requests, at least 95% approved, and at least half of any denials reversed. The report recommends; it changes nothing.

## Overrides and appeals

When a clinician approves what the engine would have denied, or an appeal overturns a denial, that is information about the policy, the evidence finding, or the requests a provider sends. Vaglio counts these **by clause** of the policy, **by provider** and **by state**. A clause behind many reversed denials is a clause to reread. A provider whose requests are often reversed may be sending incomplete records, or may be right more often than the policy allows.

## Fairness: do errors fall harder on some patients?

This is the report we take most care with, because it is the easiest to get wrong.

Approval rates *should* differ between groups sometimes. Many criteria treat ages differently on purpose: a scan indicated for older patients will be approved more often for them. So comparing approval rates between groups tells you little. What should not differ is the rate of **errors**. Vaglio's fairness report follows a published method (arXiv:2603.14631) that measures, for each group:

- of the engine's denials, how many were later reversed by a clinician or on appeal;
- how often requests were pended to ask for more evidence.

It does this by sex, age band, race and ethnicity (as the US Core standard records them), state and line of business.

A group whose rates differ from the rest more than chance would explain is flagged, using a standard statistical test (Fisher's exact test, at a strict threshold of p < 0.01). The flag is worded as "a place to read cases", not as a finding, because with many groups some will be flagged by chance.

Several protections are built in:

- **The engine never reads demographics.** The policy language has no way to read sex, race or ethnicity, and the decision never sees them. So a difference the report finds comes from the records providers sent, from reviewers, or from appeals, which is exactly where a plan should look.
- **Demographics are not copied into cases.** They are read from the stored requests only when the report is made.
- **Small groups are hidden.** Any group with fewer than 11 requests is not shown at all, so no individual can be picked out.
- **Who looked is recorded.** The report is for reviewers only, and making it is written to the audit log.
- **What it cannot see, it says.** Income and disability are not part of a prior authorization request, so the report does not pretend to measure them.

![The fairness report: reversal and pend rates by group, with small groups hidden and differences beyond chance flagged for reading.](/img/fairness.png)

## A shadow pilot: watching Vaglio before trusting it

The most important report may be the one a plan runs before Vaglio decides anything. In a **shadow pilot**, Vaglio sees every request and decides it, but its answer is not used. Technically, it decides, records its decision, and then tells the PAS server "I decide nothing for this item", so the plan's normal process answers exactly as it would have without Vaglio. Meanwhile, the plan's systems send Vaglio what the plan actually decided.

The Reports screen then shows:

- how often Vaglio and the plan agreed, with a 95% confidence interval and Cohen's kappa, a standard measure of agreement that discounts agreement by chance;
- a table of Vaglio's verdicts against the plan's;
- agreement policy by policy;
- every disagreement, listed first.

Neither side is assumed to be right. A clinician reads each disagreement and rules on it from the case: which decision the record supports, Vaglio's, the plan's, both or neither, with the reason. That ruling is recorded too.

A shadow pilot can also be run offline, over a plan's past requests and its recorded outcomes, with nothing sent anywhere and everything deleted at the end.

This is how a plan can see Vaglio on its own cases, with its own policies and its own providers' records, without letting it decide a single real request. It is also, plainly, the step no plan has taken with Vaglio yet. The live shadow mode has been run end to end through Perfuse on one machine, not yet beside a plan's real process.

## Why reports come from the log

Every report above is computed from the same append-only, tamper-evident log that holds the decisions. That has two consequences. A report cannot be quietly adjusted without the log showing it. And any figure in it can be traced back to the individual decisions behind it, each with its proof and receipt. When a regulator asks "where does this number come from?", the plan can show the cases.

Next: the [Medicare library](/learn/medicare-rules-checked/), and what checking Medicare's own rules found.
