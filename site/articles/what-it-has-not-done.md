---
title: What Vaglio has not done yet, said plainly
short: What it has not done yet
tile: No production site, no licensed criteria, no outside review, no certification. Everything we have not proved, in one place.
area: Start here
icon: flag
order: 12
description: Every important thing Vaglio has not yet done or proved, gathered in one place, so that a health plan, a regulator or a curious reader knows exactly where the evidence stops.
---
Software for health care is easy to oversell. Every claim on this site is meant to be checkable, and this page is the other half of that promise: the things Vaglio has **not** done, or has done only against stand-ins, in one place. If you are deciding whether to trust Vaglio with anything, read this page first.

## It has not decided a real patient's request

**No health plan runs Vaglio in production.** Everything on this site has been built and tested: the engine, the console, the reports, the connections. The whole path has been run end to end through a real Perfuse server, and against standard test kits. But every request it has decided was invented. It has no track record on real patients, real providers' records or a real plan's volume.

The way to change that safely is a [shadow pilot](/learn/reports-regulators-ask-for/): Vaglio decides a plan's real requests beside the plan's normal process, its answers are not used, and a clinician reviews every disagreement. Vaglio has the tools for that. No plan has run one yet.

## It ships no licensed clinical criteria

Most plans decide against InterQual or MCG criteria, which are licensed products. Vaglio does not include them and cannot share them. Its library is written from public Medicare and state documents. A plan brings its own policies, or runs its licensed criteria as a service Vaglio can ask, which needs a small adapter that no criteria vendor offers yet.

## Nobody outside BioDream has reviewed it

- **No clinician** outside BioDream has reviewed the library's policies. That is why, since version 0.2.0, a library policy decides nothing until a plan's own clinicians adopt it.
- **No security firm** has tested it. The assurance pack (a threat model, a HIPAA mapping and a hazard log) is written and published, and two language models reviewed it as an auditor would, but no human from outside has.
- **No statistician** has reviewed the fairness report's method, beyond the published paper it follows.

## It is not certified, and it is not a service

Vaglio has no SOC 2 or HITRUST report, no penetration test by an outside firm, no business associate agreement and no service levels. It is software a plan runs, not an outcome a vendor sells, and BioDream does not run it for anyone. Its connection to the PAS standard was tested with Inferno's official kit through Perfuse, but the kit certifies nothing, and Vaglio itself has no certification of any kind.

## Connections tried only against stand-ins

These are built and tested, but only against our own stand-ins made from each service's public description, because trying the real thing needs an account or partner we do not have:

- the fax services (Sinch, Documo, SRFax, RingCentral, eFax), and a fax modem on a real phone line;
- Lob, for printing and mailing letters;
- a plan's case-review (UM) system: no vendor publishes an open API, so there are no named connectors;
- Surescripts and NCPDP SCRIPT for drug requests, where some element names had to be inferred, and nothing is certified;
- sign-in through Microsoft Entra ID or Okta by a real person on a live tenant (Amazon Cognito has been tried end to end in a browser);
- a real language model's automatic approvals from attached documents: how often they survive the sampled checks is what a pilot would show.

## Limits of the engine and its tools

- **Policy size.** One policy can hold up to 16 pieces of evidence, so that every combination can be checked. Larger documents are split into one policy per service.
- **AI drafting is uneven.** In our study of 60 Medicare documents, one model drafted all 60 in a form that passed every check, the other 49. In a fresh run, about one document in eight passed where it had failed before, or failed where it had passed. And two models' drafts of the same document agreed on every combination of evidence in only 2 of 37 cases. Passing the checks shows a draft is well formed, not that it means what the document means.
- **The readings tool's reach.** Comparing two drafts covers up to 18 pieces of evidence between them; 11 pairs in the study were beyond that and were not compared.
- **CQL.** Vaglio's CQL engine passes 2,003 of HL7's 2,009 conformance tests for CQL 1.5.3. Of the 6 it does not, four need decimals of 28 digits or a decimal's written precision, one is how a time with no zone is printed, and one contradicts four other tests in the same suite. Value sets are expanded when a policy is imported, not while deciding.
- **The PAS test kit.** Two tests of 84 fail, because of an error in the published standard itself (HL7 Jira FHIR-57469); they will pass once a corrected standard is released.
- **Standby.** By default, the last few decisions before a sudden failure might not have reached the standby (0 to 3 in our drills). The setting that prevents that costs about 23% of throughput.
- **macOS.** Fewer requests can be handled at once on a Mac than on Linux, because of how macOS saves files.
- **Power cuts.** Tested by replaying simulated cuts, on two Linux file systems. A disk that claims to have saved data and then loses it defeats any software.
- **The log's protection** shows any change made after a checkpoint was witnessed outside the plan, and every answer carries a signed promise that makes a decision left out of the log provable by whoever kept the answer. It cannot say anything about a decision that was never answered at all.

## What we will not do

Some things are not on the list because we have decided against them:

- **Vaglio is source-available, and we describe it only that way.** Its licence is the PolyForm Shield License 1.0.0: anyone may read, use, change and share it, a plan deciding its own requests included, but not to sell a product that competes with it.
- **We do not accept outside contributions.** Pull requests are not reviewed or merged. Bug reports and private security reports are very welcome.
- **We will not ship AHIP's documentation standards**, because AHIP's terms of use do not allow them to be copied or adapted.
- **We will not let missing evidence deny care**, whatever a policy says. That rule is not a setting.

## How this page stays honest

This page is built from the same repository as the software, and it changes when the facts do. The repository's README has a "Status" section and a "What you can check for yourself" section with the same information in more technical form, and every claim there names the test or the document that shows it.

If something here is out of date, or something we claim elsewhere is not supported, please [open an issue on GitHub](https://github.com/BioDream-LLC/vaglio/issues). We would rather be corrected than believed.
