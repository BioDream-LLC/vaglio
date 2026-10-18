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

## What this shows, and does not

- The language could express 22 of 60 real LCDs with a mid-sized open model and four rounds, holding every clause to
  the LCD's own words. Most failures were the model failing the quote gate, not the language failing the LCD.
- One limit of the language is real: 16 pieces of evidence per policy, which 3 of the 60 needed more than. Splitting
  such an LCD into one policy per service it covers is the way round it today.
- **A passing draft is not a correct policy.** The gates check form, quotes, codes and consistency, not meaning.
  Nobody has read these drafts against their LCDs. Each would need a clinician's sign-off, as every draft does, before
  it decided anything. The drafts are not in the repository.
- Better models would likely pass more; that was not measured here.
