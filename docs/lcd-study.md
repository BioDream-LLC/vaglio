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

## Third round: one policy per service, and the DME articles' codes (2026-10-24)

The second round's failures pointed at two more things, again the study's and the drafting loop's, not the language's:

- **A document that holds different services to different criteria** (a gastric and a respiratory suction pump; a
  splenectomy and four drugs for ITP) cannot be one policy: a policy's approval covers every code it lists, so the model
  wrote an approval for one service beside a denial for another, and the checker rightly called that a contradiction.
  The drafter now accepts one policy per service, each in its own block, its id the given one with a suffix, each gated
  alone, and a code in only one of them (guide rule 9, `draft.GateAll`). The console and `vaglio draft` save each.
- **The DME contractors' billing articles list their codes in the article's own text**, not under the Coverage API's
  code endpoint, so the study gave the support surfaces LCD no codes at all. It now reads the HCPCS code numbers from
  the article's text when the endpoint has none (numbers only).

Every LCD that had not passed with a model was drafted again with it:

| | Llama 3.3 70B | DeepSeek V4 Pro |
|---|---|---|
| Passed every gate, round 2 | 43 | 44 |
| Passed every gate, round 3 | **48** | **59** |
| Drafted as several policies | 0 | 2 (Suction Pumps: 8, one per HCPCS code; ITP therapy: 5) |

59 of the 60 passed with one model or the other; only Cervical Disc Replacement (L38033) passed with neither (DeepSeek:
a contradiction in its own reading of the two-level rule; Llama: a clause not word for word). The support surfaces LCD
passed with both once it had its codes. Some of the gain is the reruns' own variance: the split and the codes explain
three of DeepSeek's fifteen new passes for certain; the rest were drafted again with the same guide and feedback and
passed where they had not, which is what a model's run-to-run variance looks like. One LCD (L38268) needed DeepSeek
longer than the five minutes a model is given per answer and passed with fifteen (`lcdstudy -model-timeout`).

## The last LCD, the same runs again, and whether the readings mean the same (2026-10-24)

**Cervical disc replacement (L38033)**, the one LCD neither model drafted, failed DeepSeek eight rounds running on one
clause: the LCD writes its indication as a sentence running on through a list ("The Beneficiary has EITHER:", two
bulleted items, "OR"), and the model quoted it whole, its bullets left out, which the quote gate refused as not word
for word. That was the gate's fault: the words are the LCD's, in its order. The gate now accepts a clause the document
holds once its list items' markers are left out (`draft.Quoted`; a clause the document does not hold is still
refused). With it, DeepSeek drafted L38033 in two rounds: **60 of 60 with DeepSeek**, and Llama's failures drafted
again passed one more, **49 of 60**.

**The same runs again.** Each figure above was one run, and later rounds drafted only what had not passed. To see how
much is the models' variance, each model drafted all 60 again from nothing, with the third round's guide and feedback
(and the old quote gate), in one round of up to four drafts each:

| | Llama 3.3 70B | DeepSeek V4 Pro |
|---|---|---|
| Passed, fresh run | 45 | 50 |
| Passed in the earlier rounds, failed this time | 6 | 10 |
| Failed in the earlier rounds, passed this time | 3 | 1 |

So about one LCD in eight passes or fails by the run, and the cumulative figures (49 and 60) owe something to drafting
again what failed. The fresh runs' failures were mostly lint and gaps (a decision that some combination of evidence
leaves without an answer), not quotes.

**Do two readings mean the same?** The gates check form, not meaning. One check of meaning that needs no person: give
the same LCD to two drafters and compare what they decide, combination by combination of the evidence they share
(`vaglio readings`, which pairs evidence by the clause it quotes). Where both models passed an LCD (48), 37 could be
compared (11 have more than 18 pieces of evidence between the two readings, past what `readings` compares; one pair
quotes no clause in common):

| Two readings of one LCD | Pairs | Decide alike on every combination | Median share of combinations they decide differently | Within 5% |
|---|---|---|---|---|
| Llama against DeepSeek | 37 | 2 | 3.9% | 19 |
| Llama against itself (earlier round and the fresh run) | 37 | 7 | 1.2% | 25 |
| DeepSeek against itself | 40 | 8 | 2.0% | 24 |

Two models (and one model twice) mostly agree, and almost never entirely: they choose different clauses as evidence
(the two models shared 4.6 clauses on average, beside 2.1 and 3.7 each chose alone), and where they choose the same,
they sometimes combine them differently. Some of that is the LCDs' own ambiguity, which `vaglio readings` exists to
show (docs/medicare-readings.md); some is a model's misreading. Telling the two apart needs a person who reads the LCD.
What the comparison gives that person is where to look: the combinations the two readings decide differently.

## What this shows, and does not

- The language expressed all 60 real LCDs (60 with DeepSeek alone, after the quote gate learned to read a sentence
  through its list), holding every clause to the LCD's own words; two of them as one policy per service.
  The first run's failures were mostly the drafting loop's: clearer feedback roughly doubled the passes with the same
  model.
- The 16-evidence limit looked like a real limit in the first run, but it was the model writing the absence of a fact
  as evidence of its own; told otherwise, both models fitted every LCD.
- **A passing draft is not a correct policy.** The gates check form, quotes, codes and consistency, not meaning.
  Two independent readings of one LCD agree on every combination of evidence in only 2 of 37 comparable cases (the
  median differs on 3.9% of combinations). Nobody has read these drafts against their LCDs. Each would need a clinician's sign-off, as every draft does, before
  it decided anything. The drafts are not in the repository.
- Two models, one sample of 60, four rounds each (eight for the last LCD), three study rounds and a second fresh run.
  The models' answers vary from run to run: about one LCD in eight passes or fails by the run.
