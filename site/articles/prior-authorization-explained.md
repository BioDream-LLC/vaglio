---
title: Prior authorization, explained from the beginning
short: What prior authorization is
tile: Who asks whom for what, why plans do it, why doctors and patients hate it, and why AI denials are now in court.
area: Start here
icon: clipboard
order: 1
description: What happens when a doctor has to ask a health plan's permission before treating you, why it exists, what goes wrong, and what the new federal rules change.
---
Before some treatments, your doctor has to ask your health plan whether it will pay. That request is prior authorization. It is one of the least loved parts of American health care, and in 2026 it is also one of the most watched. This article explains it from the start: who asks whom, what the plan looks at, why the system exists at all, what goes wrong, and what changed when computers began to answer.

## The basic exchange

Three parties take part:

- **The patient**, who needs a test, a procedure, a device or a drug.
- **The provider**, the doctor's office, hospital or clinic that will deliver it.
- **The plan**, the insurer or government program that will pay for it: a commercial insurer, a Medicare Advantage plan, a state Medicaid program.

For most care, the provider treats the patient and sends the bill afterwards. For a list of services the plan chooses, the order is reversed. The provider must ask first, and the plan must say yes before the service is covered. If the provider skips the step, the plan can refuse to pay even if the care was entirely appropriate.

Which services need it varies by plan. Common examples are MRI and CT scans, joint replacements, spine surgery, sleep studies and the CPAP machines that follow them, power wheelchairs, some injections, and many expensive drugs.

## What the plan actually checks

A prior authorization request says what the patient needs, why, and what the record shows. The plan compares it with a written **coverage policy** for that service. A coverage policy is a document that lists the situations in which the plan considers the service medically necessary. For a lumbar spine MRI, for example, a typical policy covers the scan when:

- a "red flag" is suspected, such as signs of nerve compression, infection, cancer or a fracture; or
- the patient has had at least six weeks of conservative care, such as physical therapy or medication, without relief; or
- the scan is needed to plan a surgery that has been scheduled.

It may also refuse a repeat scan within six months unless something has changed.

Each of those conditions is a piece of **evidence** the plan looks for in the patient's record. The plan's answer is one of three:

- **Approved.** The record shows the policy is met.
- **Denied.** The record shows it is not met. The provider can appeal.
- **Pended**, meaning on hold. Something is missing, and the plan asks for it.

Where do the policies come from? For traditional Medicare, they are public. Medicare publishes National Coverage Determinations, which apply everywhere, and Local Coverage Determinations, written by the contractors who process claims in each region. Medicare Advantage plans must follow those, and may add their own criteria where Medicare's are silent. Commercial plans write their own, and many license detailed criteria from two companies, InterQual and MCG. Those licensed criteria are not public.

## Why plans do it

Plans give three main reasons, and each has some truth in it:

1. **Cost.** Some services are expensive, and some are ordered more often than the evidence supports. Asking first lets the plan stop payment for care it considers unnecessary before it happens.
2. **Safety and appropriateness.** A request can be checked against the evidence: the right imaging for the symptom, the cheaper drug tried first, the dangerous combination caught.
3. **Fraud and abuse.** A check before payment is harder to game than one after.

## Why doctors and patients dislike it

The problems are just as real:

- **Time.** In the American Medical Association's 2024 survey, physicians reported about 39 prior authorization requests a week, and about 13 hours of their own and their staff's time spent on them.
- **Delay.** A patient waits while the request is reviewed. For a scan or a surgery, a delay can mean days of pain or a worse outcome. Some patients give up on the treatment entirely.
- **Paperwork.** Requests still travel by fax and phone as often as by computer. The same evidence is typed in again and again, in a different form for each plan.
- **Opaque answers.** A denial often says little more than "not medically necessary". The provider has to guess what was missing, gather it, and try again or appeal.
- **Denials that do not hold up.** When denials are appealed, many are overturned, which suggests that many should not have been issued in the first place.

## When computers started answering

Plans receive millions of requests a year, and they have increasingly used software to decide them, or to sort them before a person looks. Some of that software is simple rules. Some of it is machine learning: a model trained on past data that predicts an answer.

Automation can help everyone. A request that clearly meets the policy can be approved in seconds instead of days. But a model's prediction is not the same thing as a reason. When a model says "deny", it may not be able to say which part of the policy was unmet, or what in the record it relied on. That is the problem now being argued in public:

- **In court.** In *Estate of Gene B. Lokken v. UnitedHealth Group*, in federal court in Minnesota, the plaintiffs say an algorithm called nH Predict was used to deny Medicare Advantage members care after a hospital stay. In March 2026 the court ordered broad discovery into how it worked.
- **In the Senate.** In July 2026 Senators Blumenthal and Hawley asked UnitedHealthcare, Humana and CVS for records on how they use AI and prior authorization to deny care.
- **In the states.** More than a dozen states, California, Texas, Arizona and Maryland among them, now limit how insurers may use AI in these decisions. Most require that a clinician make or review any denial. KFF keeps a summary of these laws.

Every one of these asks the same question: *why was this request denied, and can you show it?*

## What the new federal rule requires

In January 2024 the Centers for Medicare & Medicaid Services (CMS) published the Interoperability and Prior Authorization Final Rule, known as CMS-0057-F. It covers Medicare Advantage plans, Medicaid and the Children's Health Insurance Program, and plans on the federal marketplace. In plain terms it requires:

- **Deadlines.** From January 2026, Medicare Advantage, Medicaid and CHIP must answer within 7 calendar days for a standard request and 72 hours for an urgent one. (Marketplace plans keep the deadlines they already had; the rest of the rule covers them too.)
- **A specific reason for every denial**, so the provider knows what to fix.
- **A standard electronic way to ask.** By January 2027, plans must offer a Prior Authorization API, a standard connection that a provider's computer system can use directly, built on the HL7 FHIR standard. The Da Vinci Project's "PAS" specification describes how such a request and answer look.
- **Published numbers.** Plans must publish each year how many requests they approved, denied and approved on appeal, and how long they took.

Separately, in 2025 the industry's two large associations, AHIP and the Blue Cross Blue Shield Association, pledged to reduce how many services need prior authorization and to standardize what documentation plans may ask for.

## Where Vaglio fits

A plan that must answer within 7 days, give a specific reason for every denial, take requests through a standard API, and publish its numbers needs software that does all four, and does them in a way that can be shown to a court or a regulator. Vaglio is built for exactly that. It is the decision engine that sits behind a plan's prior authorization server:

- It reads the evidence in each request and applies the plan's own written policy to it.
- Its answer is a **proof**: every step quotes the policy and points to the place in the record it relied on.
- It **never denies because something is missing**. A gap in the record means a pend that names the missing evidence, never a denial.
- It keeps every decision in a **log** that shows if anything was changed afterwards, and builds the numbers the plan must publish from that log.
- Whatever the rules cannot settle, it gives to a **person**, and a plan can require that only a clinician ever denies.

Vaglio sits behind a prior authorization server such as [Perfuse](https://perfuse.health), BioDream's free server for the PAS standard, which handles the electronic exchange with providers. Vaglio supplies the decision. A plan can also take requests that arrive by fax, phone or portal and decide them the same way.

## Words you will meet in the other articles

| Word | What it means |
|---|---|
| Plan, payer | The insurer or program that pays for care |
| Provider | The doctor, hospital or clinic that gives the care |
| Coverage policy | The plan's written rules for when a service is covered |
| Evidence | A fact in the patient's record that the policy asks about |
| Pend | Put a request on hold and ask for what is missing |
| LCD, NCD | Medicare's local and national coverage documents |
| PAS | The Da Vinci standard for prior authorization by API |
| FHIR | The HL7 standard health data is exchanged in |
| Utilization management (UM) | The plan's department, and its software, that reviews requests |

The next article explains, step by step, [how Vaglio turns a request into an answer](/learn/how-vaglio-decides/).

Sources for the figures above: the American Medical Association's 2024 prior authorization physician survey; the court docket in *Lokken v. UnitedHealth Group* (D. Minn., 0:23-cv-03514); the senators' press release of July 2026; KFF's summary of state laws on AI in prior authorization; and CMS's text of CMS-0057-F.
