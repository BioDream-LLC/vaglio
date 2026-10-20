# Does the policy language fit real coverage policy?

A study of 60 Medicare Local Coverage Determinations, October 2026. The question is whether Vaglio's way of writing
policy holds up against real, complicated coverage text, or only against policies written for it.

## Method

- **Sample.** Every LCD in force from the CMS Coverage API, one per title (several contractors publish the same one),
  in order of document number, every n-th taken: 60 of 461. The sample was not chosen.
- **Text.** Each LCD's "Coverage Indications, Limitations, and/or Medical Necessity" section, as plain text, with the
  code numbers from its billing and coding articles (no CPT descriptions).
- **Drafting.** Each LCD went through `vaglio draft`'s loop: a model (Llama 3.3 70B Instruct, through OpenRouter)
  writes a policy, and Vaglio's gates check it. A draft passes only if it parses, quotes every clause word for word
  from the LCD, uses only codes the LCD lists, decides every combination of its evidence without contradicting itself,
  and has no lint warnings. A draft that fails gets the failures back and tries again, four rounds at most.
- **Repeatable.** `go run ./tools/lcdstudy -n 60` runs it again. The LCDs change; the model's answers vary.

## Results

| | LCDs |
|---|---|
| Drafted to a policy that passed every gate | **22** (37%) |
| Every round failed | 37 |
| Too long for the model's context | 1 (Outpatient Physical Therapy) |

Of the 22 that passed, 9 passed in the first round and 10 in the second. They had a median of 7 pieces of evidence and
15 rules. They range from Therapeutic Shoes for Persons with Diabetes and Surgical Dressings to Total Shoulder
Arthroplasty, Visual Electrophysiology Testing (15 pieces of evidence) and two MolDX genetic tests.

Why the 37 failed in their last round (an LCD can fail on more than one, so the rows add up to more than 37):

| Failure | LCDs | Whose limit it is |
|---|---|---|
| A clause not quoted word for word | 23 | The model's. The gate is doing its job: it refuses a clause the LCD does not say. In the one case traced word by word (L33252), the model ended a sentence at a comma with a full stop: 36 of its 37 words matched. |
| Lint (a rule that can never fire; evidence that changes nothing), other than the next row | 8 | Mostly the model's: logic that does not hang together. |
| More than 16 pieces of evidence | 3 (Pressure Reducing Support Surfaces Group 3, rTMS in adults, Proton Beam Therapy) | **The language's.** The checker tries every combination of evidence, so a policy is capped at 16. These LCDs need more, or need splitting into several policies. |
| Codes the LCD does not list | 3 | The model's. |
| Every combination not decided | 2 | Either. A gap may be the LCD's own: `vaglio readings` reports those as possible ambiguities. |
| Syntax | 1 | The model's. |

## Second round: better feedback, a second model (2026-10-19)

The first run's failures pointed at two things Vaglio could do better, both in the drafting loop, not the language:

- **The quote gate now says where the model went wrong.** A clause that is not in the LCD comes back with the LCD's
  nearest sentence (the one sharing the most words, when it shares at least half), so the model can copy it.
  It is advice in the failure message only: the gate still accepts nothing but the LCD's own words.
- **The guide now says how to stay within 16 pieces of evidence.** The absence of a fact is never evidence of its own
  (the model had written both `x` and `no_x` as evidence), and a list of alternatives with the same effect may be one
  piece of evidence with a `find` line for each.

The same 60 LCDs, the 38 that had not passed drafted again:

| | Llama 3.3 70B, first run | Llama 3.3 70B, better feedback | DeepSeek V4 Pro, better feedback |
|---|---|---|---|
| Passed every gate | 22 | **43** | **44** (23 in the first round) |
| Failed the quote gate in the last round | 23 | 5 | 2 |
| Needed more than 16 pieces of evidence in the last round | 3 | 0 | 0 |
| Too long for the model | 1 | 1 | 0 (its context is far larger) |

53 of the 60 passed with one model or the other. DeepSeek's largest passing policy uses exactly 16 pieces of evidence.
The 16 limit stays: with the guidance, every LCD in the sample fitted under it.

The 7 neither model passed, and what stopped them in the last round:

| LCD | What failed |
|---|---|
| Suction Pumps | contradiction, lint |
| Pressure Reducing Support Surfaces - Group 3 | a code the study's text lacked (below) |
| Cervical Traction Devices | gap, lint |
| rTMS in adults | lint, syntax |
| Upper Gastrointestinal Endoscopy | gap, lint (quote with Llama) |
| Cervical Disc Replacement | contradiction, lint (quote with Llama) |
| Immune Thrombocytopenia Therapy | contradiction, lint |

The support surfaces failure is the study's, not the model's. DeepSeek named the service by its HCPCS code, and the
code gate refused it because the code was not in the text it was given. That LCD's billing articles have no codes under
the Coverage API's article code endpoint, so the study gave none. A gap or contradiction may be the LCD's own:
`vaglio readings` reports those as possible ambiguities, and nobody has checked these.

## What this shows, and does not

- The language expressed 53 of 60 real LCDs (44 with one model alone), holding every clause to the LCD's own words.
  The first run's failures were mostly the drafting loop's: clearer feedback roughly doubled the passes with the same
  model.
- The 16-evidence limit looked like a real limit in the first run, but it was the model writing the absence of a fact
  as evidence of its own; told otherwise, both models fitted every LCD.
- **A passing draft is not a correct policy.** The gates check form, quotes, codes and consistency, not meaning.
  Nobody has read these drafts against their LCDs. Each would need a clinician's sign-off, as every draft does, before
  it decided anything. The drafts are not in the repository.
- Two models, one sample of 60, four rounds each. The models' answers vary from run to run; these are single runs.
