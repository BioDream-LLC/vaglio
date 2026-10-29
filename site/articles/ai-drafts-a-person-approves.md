---
title: "AI drafts, a person approves: turning a coverage document into a policy"
short: AI drafts, a person approves
tile: A language model can write the first draft of a policy, quoting the document word for word. Nothing decides until a clinician signs it off.
area: Deciding
icon: pen
order: 5
description: How Vaglio uses a language model to draft a policy from a coverage document, the gates every draft must pass, what we measured on 60 real Medicare documents, and why the model never has the last word.
---
Writing a coverage policy as precise rules takes time. A Medicare coverage document can run to many pages, with conditions nested inside conditions, lists of codes in separate billing articles, and exceptions in footnotes. Vaglio can hand the first draft to a language model, the same kind of AI behind chat assistants. But it treats that model the way a careful editor treats a new writer: every sentence is checked against the source, every rule is tested, and nothing is published until a qualified person has read it and signed it.

## Why use a model at all

The hard part of turning a document into rules is not the logic; it is the reading. Someone has to find every condition, notice which ones are joined by "and" and which by "or", collect the codes, and write each rule in the policy language. A model is good at a first pass of that kind of reading. It is fast, it does not get tired on page forty, and it produces a complete draft that a clinician can correct, which is much quicker than starting from nothing.

What a model is not good at is being right every time. It can misread a sentence, invent a condition, drop a code, or paraphrase where the exact words matter. So everything below is about catching those mistakes automatically, and then putting a person in charge of what is left.

## The drafting loop

`vaglio draft` (or the console's "Draft from a document") takes a coverage document as plain text and a model to use. It works in rounds:

1. The model is given the document, a guide to Vaglio's policy language, and the job: write a policy that says what this document says.
2. Vaglio puts the draft through its **gates**, the checks below.
3. If any gate fails, the model gets the exact failures back and tries again. After four rounds at most, the drafting stops and says what is still wrong.

A document that covers several services with different criteria, such as a suction-pump document with one rule for one kind of pump and another rule for another, can be drafted as one policy per service, each checked on its own.

## The gates

A draft passes only if every one of these holds:

- **It parses.** The draft is valid in the policy language.
- **Every clause is a quotation.** Every rule carries the sentence of the document it stands for, and that sentence must appear in the document *word for word*. A paraphrase is refused, however close. When a quotation fails, the model is shown the document's nearest sentence so it can copy it exactly; the gate still accepts nothing but the document's own words.
- **Only codes the document lists.** A procedure or diagnosis code the document and its billing articles do not mention is refused. The model cannot invent codes.
- **It decides every situation, without contradicting itself.** The draft is run through [the full check](/learn/every-situation-checked/): every combination of its evidence, no answer that is both approve and deny, no combination left without an answer, no decision that can never be reached.
- **No warnings.** No rule that can never matter, no evidence that cannot be found.

The quotation gate deserves a word. It is what keeps the model honest. A model cannot quietly change "at least six weeks" to "six weeks or more of therapy and imaging" if every clause must be found, letter for letter, in the source. And because each rule carries its sentence, a reviewer reading the draft can always see exactly what part of the document a rule claims to encode.

## What we measured: 60 real Medicare documents

To find out whether this works on real coverage text, not on examples written for it, we ran a study in October 2026. We took Medicare's Local Coverage Determinations in force at the time, one per title, and picked every n-th one in document order: 60 of 461, a sample nobody chose. Each went through the drafting loop with two models, Llama 3.3 70B and DeepSeek V4 Pro. The full method, every table, and the tool to run it again are in [docs/lcd-study.md](https://github.com/BioDream-LLC/vaglio/blob/main/docs/lcd-study.md).

The first run was humbling: Llama's drafts passed every gate for 22 of the 60. The failures pointed at things to improve, almost all in the drafting loop rather than the policy language:

- The quotation gate now tells the model which sentence it should have copied.
- The guide now explains that the absence of a fact is never its own piece of evidence, and how to keep a long list of alternatives as one piece of evidence.
- A document that holds different services to different criteria may be drafted as one policy per service.
- A clause that runs on through a bulleted list is accepted when its words, in order, are the document's.

After three rounds of improvement:

| | Llama 3.3 70B | DeepSeek V4 Pro |
|---|---|---|
| First run | 22 of 60 | not run |
| After the improvements, drafting again what failed | 49 of 60 | 60 of 60 |
| One fresh run of all 60 from nothing | 45 of 60 | 50 of 60 |

The last row matters. Models vary from run to run: in a fresh run, about one document in eight passed where it had failed before, or failed where it had passed. The higher figures owe something to simply trying again. We report both.

## Passing the gates is not the same as being right

This is the most important finding, and we want to state it plainly. The gates check *form*: the draft quotes the document, uses its codes, and is consistent. They cannot check *meaning*: whether the draft reads the document the way its authors intended.

So we checked meaning a second way. Where both models produced a passing draft of the same document, we compared the two drafts on every combination of evidence they share, using `vaglio readings`. Two careful readers of the same document should mostly agree:

| Two drafts of one document | Pairs compared | Decide alike on every combination | Median share of combinations decided differently |
|---|---|---|---|
| Llama against DeepSeek | 47 | 2 | 3.9% |
| Llama against itself, two runs | 41 | 7 | 1.2% |
| DeepSeek against itself, two runs | 44 | 8 | 1.2% |

The drafts mostly agree, and almost never entirely. On the largest documents they can differ widely: on one shoulder surgery LCD the two models disagree on almost every combination, largely because one model wrote five diagnoses as five pieces of evidence and the other as one, so the comparison cannot pair them. Some of the differences are the documents' own ambiguities: sentences that honestly admit two readings. Some are a model's misreading. Telling them apart needs a person who reads the document. What the comparison gives that person is where to look: the exact situations in which the two drafts disagree.

That comparison is built into the drafting. With **draft twice** (`vaglio draft -twice`, or `serve -draft-twice` for the console), every document is drafted a second time, by the same model or a second one, and the two readings are compared on every combination of evidence before anyone sees them. When they agree, the draft says so. When they do not, the console shows each disagreement with the sentence each draft rests on and the simplest situation that shows it, and the saved draft begins with the same list. The models stay as uneven as the table shows; the difference is that the unevenness is shown, never hidden behind one lucky run.

## The person in charge

That is why no draft, from a model or from a person, decides anything on its own:

- **A draft is a draft.** It is saved as a draft and the server will not use it.
- **A clinician signs it off.** A plan's own policy decides only after a clinician signs it off in the console, with their name, NPI and credential recorded and the exact text fingerprinted.
- **Library policies wait for adoption.** The policies in Vaglio's built-in library were written from public Medicare documents, some first drafted by a model in this way, and checked against their sources by BioDream, not by a clinician. So since version 0.2.0 each one decides nothing for a plan until that plan's own clinicians adopt its exact text. See [clinicians stay in charge](/learn/clinicians-stay-in-charge/).
- **The review is laid out for them.** The console's review packet puts the source document beside each clause, shows the check, the policy map and what changed since the last version, so the clinician's time goes on judgment, not on searching.

## Documents attached to a request

The same principle applies to the other place a model can help: reading the notes and reports a provider attaches to a request. A model can propose evidence from them, but every proposed finding must quote the document word for word, just like a clause. A reviewer confirms it, or, where the plan allows, it may approve a request on its own while people check a sample of those approvals. A finding read by a model can never cause a denial.

## Privacy

Drafting a policy sends the coverage document to the model's provider, so it is meant for published coverage documents only, never anything with patient data in it. Reading attached documents can use a model the plan runs itself, so the patient's records stay on the plan's machines.

Next: how anyone can check a decision afterwards, with [a receipt and a tamper-evident log](/learn/receipts-anyone-can-check/).
