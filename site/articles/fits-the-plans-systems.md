---
title: How Vaglio fits a health plan's systems
short: Fits the plan's systems
tile: The Da Vinci PAS standard through Perfuse, questionnaires and CQL, faxes and portals, drug requests, and the plan's own case-review system.
area: Running it
icon: plug
order: 10
description: Every way a request can reach Vaglio and every way its answer leaves, the standards involved explained in plain words, and an honest note on which connections have been tried against real systems and which only against stand-ins.
---
A health plan is not a blank slate. It already has a front door for prior authorization requests, a member file, a provider network, a case-review system where its nurses and physicians work, fax lines, letter printing and sign-in for its staff. A decision engine that asked the plan to replace all of that would never be used. Vaglio is built to slot in behind what the plan has, and to speak the standards the plan's partners already speak. This article goes through each connection, in plain words, and says which ones have been tried for real.

## The front door: Perfuse and the PAS standard

The CMS rule requires most government-program plans to accept prior authorization requests through a standard API by January 2027. The standard is HL7's Da Vinci **PAS** (Prior Authorization Support) specification, built on **FHIR**, the modern format health data is exchanged in. A provider's system sends a request as a bundle of FHIR records; the plan answers in the same format.

Vaglio does not sit on the internet answering providers directly. That is the job of a PAS server: it accepts the connection, checks who is calling, translates to and from the older X12 278 format that many systems still use, handles subscriptions so the provider hears when a pended request is decided, and passes each request to a decision service. Vaglio is that decision service.

BioDream's PAS server is [Perfuse](https://perfuse.health), which is free under the Apache-2.0 licence. Perfuse asks Vaglio about every request its own rules leave to review. Vaglio answers with the decision, the reason and the proof. If Vaglio has no policy for a service, it says so and Perfuse answers as it would without it. If anything fails, Perfuse pends, which is the safe answer. Perfuse never requires Vaglio, and Vaglio works behind any PAS server that calls the same decision hook.

How do we know it holds up? The official test kit for PAS servers, Inferno's Da Vinci PAS test kit, was run against Perfuse with Vaglio deciding behind it. The result was 82 tests passed and 2 failed, the same as Perfuse alone. The two failures are in the published standard itself: three elements it marks as required are defined in a way that forbids them where it requires them. HL7 and the kit's maintainers have accepted this as an error in the guide (HL7 Jira FHIR-57469), and a fix waits for a corrected release. The kit tests the PAS server; the result shows Vaglio leaves that conformance unchanged, not that Vaglio itself is certified.

## Before the request: questionnaires and CQL

Two other Da Vinci standards come before the request is sent:

- **CRD** (Coverage Requirements Discovery): while the doctor is placing an order, their system asks the plan "does this need prior authorization, and what do you need?" Perfuse answers. With Vaglio behind it, it can also say what the answer would be as the record stands: would be approved, would wait for these items, or would not be approved and what would change that.
- **DTR** (Documentation Templates and Rules): the plan supplies a **questionnaire** for the service, and the provider's system fills in what it can from the chart by itself. The logic that does the filling-in is written in **CQL** (Clinical Quality Language), HL7's standard language for clinical rules.

Vaglio builds each service's questionnaire from the same policy file it decides with, so the questions and the decision cannot drift apart. For a plan that already has DTR packages, `vaglio import-dtr` turns one into a draft Vaglio policy: each yes-or-no question becomes evidence, and each question's CQL is run alongside it, so the record and the provider's answer are read side by side. Where they disagree, the case says so.

Vaglio has its own CQL engine, written for it. On HL7's official CQL conformance tests for CQL 1.5.3 it passes 2,003 of 2,009. On the Da Vinci reference home-oxygen package, it gives the same answers as the reference CQL engine on every one of the 79 definitions we compared, on two test patients. Value sets, the named lists of codes CQL refers to, can be expanded from a terminology server such as tx.fhir.org, or from the NLM's VSAC with the plan's own UMLS key.

## Requests that do not arrive by API

Many requests still come by fax, by phone or by post, and some through the plan's portal. All of them reach the same engine:

- **Intake.** Staff key a fax, call or letter into Vaglio's intake form. It becomes a request just like one that arrived by API, decided by the same policy, with the same proof and the same deadlines.
- **The fax inbox.** Faxes can arrive from the plan's fax service: Sinch (Phaxio), Documo, SRFax, RingCentral and eFax are supported, as is a scan folder where any scanner or fax server drops files. Vaglio can also answer a fax line itself through a standard fax modem. Each fax is turned into pictures of its pages and, where a text-recognition program is installed, into text, ready for intake.
- **The provider portal.** Providers can sign in to see their own requests, check what is missing, and send it.

## Drug requests

Prescription drugs have their own prior authorization standard: **NCPDP SCRIPT** electronic prior authorization, used between prescribers' systems and pharmacy benefit networks such as Surescripts. Vaglio reads and answers these messages, with question sets and step-therapy rules. This was built from Surescripts' public guide; some element names inside the question sets had to be inferred, and it is not certified by NCPDP or Surescripts.

## The plan's own data

- **Eligibility.** Vaglio can read the plan's member file to check that a patient is covered on the day of service, and answers the FHIR eligibility request. A patient it cannot confirm is pended, never denied.
- **The provider network.** A policy can require an in-network provider, checked against the plan's network file.
- **Provider specialty.** A policy can require a specialty, checked live against the national NPI registry.
- **Licensed criteria.** As [the library article](/learn/medicare-rules-checked/) explains, a plan's licensed criteria can be asked through a criteria service the plan runs.

## The plan's case-review system

Most plans already have a **utilization management (UM) system**, where their nurses and physicians work cases. Vaglio does not ask them to change screens. For every request it pends or proposes to deny, it can open a case in the plan's UM system with everything attached: the proof, the missing evidence, the clauses, the receipt, the due date, a link back to Vaglio's case, and the proof as a PDF. When the reviewer decides in their own system, the decision comes back to Vaglio and is recorded as their review, with their name and credential, under the same rules as a review made in Vaglio's own console.

The connection can be configured without code: either plain REST with a map of which field goes where, or the FHIR Task standard. The UM system stays the record of the human decision, and Vaglio never closes a case there on its own. No UM vendor we looked at publishes an open API, so there are no named connectors yet; this has been tested against a stand-in server only.

## Answers going out

- **Letters.** Approval, denial and pend letters are written from the proof by template, with no model involved, in English or Spanish, with the plan's own addresses, deadlines and appeal rights, and the receipt.
- **Mailed notices.** Letters can be written as PDFs to a folder for the plan's mail house, or sent through Lob's printing and mailing service.
- **Feeds.** Every decision can be pushed to the plan's other systems as a signed webhook, or exported as a file of one record per line. Outcomes decided elsewhere, and appeal results, can be fed back in.
- **Back to the provider.** When a reviewer decides a pended case, the answer goes back through the PAS server, and the provider's subscription delivers it.

## Staff sign-in, and several plans on one server

Staff can sign in with the plan's own identity system, using OpenID Connect or SAML, such as Microsoft Entra ID, Okta or Amazon Cognito. One Vaglio server can also serve several plans, each with its own hostname, data, accounts, policies and sign-in, kept apart; a sign-in for one plan can never reach another's.

## What has been tried for real, and what has not

We want to be exact about this:

| Connection | Tried against |
|---|---|
| PAS through Perfuse | Perfuse itself, and the Inferno PAS test kit (82 pass, 2 fail, as Perfuse alone) |
| CQL | HL7's conformance tests; the reference CQL engine on the Da Vinci home-oxygen package |
| Let's Encrypt certificates | Let's Encrypt itself |
| Sign-in with OpenID Connect | Amazon Cognito, a person signing in through a browser |
| Sign-in with SAML | Responses shaped like Microsoft Entra ID's, and one captured from Entra; not yet a person on a live tenant |
| Fax services, Lob, the fax modem | Stand-ins built from their published descriptions only |
| NCPDP SCRIPT | Messages built from Surescripts' public guide; not certified |
| UM systems | A stand-in server only |

Trying the rest needs accounts and partners that only a plan, or a plan's vendors, can provide.

Next: [running Vaglio on the plan's own servers](/learn/runs-on-the-plans-servers/), and what it takes to keep it safe.
