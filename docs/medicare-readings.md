# Two readings of the same Medicare rule: where they part

Vaglio turns a coverage document into a policy whose every clause is quoted word for word from the document, and checks
the policy on every combination of evidence. That check proves a policy is consistent; it cannot prove the policy says
what the document means. So we tried something else: write a second reading of the same text, just as faithful to its
words, and let the computer find every patient record the two readings decide differently.

Where two careful readings of one sentence disagree, the sentence admits both. That is a possible ambiguity, or a
mistake in one reading, and only someone who knows the clinical intent can say which. Nothing below says Medicare, or
any contractor, wrote anything wrong.

Everything here can be rerun: `vaglio readings A.vaglio B.vaglio`, with the files in the repository's `library/`.

## Total knee arthroplasty (LCD L36575, Noridian)

The LCD lists the criteria for advanced joint disease (imaging, pain or disability, conservative therapy addressed),
then six findings joined by "or": failure of a previous osteotomy, distal femur fracture, malignancy, failure of a
previous unicompartmental replacement, avascular necrosis, proximal tibia fracture.

- Reading A (`library/tka.vaglio`): each of the six is an indication on its own.
- Reading B (`library/readings/tka.b.vaglio`): each of the six stands in for the imaging criterion only; pain or
  disability, and conservative therapy addressed, are still needed.

Both quote every clause word for word and pass every check. They decide 630 of the 16,384 possible records differently
(3.8%), always the same way: A approves, B denies. The simplest such record is a failed osteotomy and nothing else
documented.

The question a clinician answers: does a patient with a failed osteotomy, or a distal femur fracture, need documented
pain and a conservative-therapy history before a total knee replacement is covered?

## CPAP for obstructive sleep apnea (LCD L33718)

The LCD's criterion B is a sleep test with either an AHI or RDI of 15 or more, or an AHI or RDI of 5 to 14 "and
documentation of" symptoms or of hypertension, heart disease or stroke. On the web page the nesting is shown by
indentation. In the plain text of the same page it is lost, and a machine reading
(`library/drafts/cpap_osa.machine.vaglio`, drafted by a language model, passing every check) required the symptoms or
conditions for every sleep test. The library's reading (`library/cpap_osa.vaglio`) requires them only for 5 to 14.

The two readings decide 512 of 4,096 records differently (12.5%). The largest group, 352 records, is approved by the
library's reading and denied by the machine's: a patient with an AHI of 15 or more and no listed symptom.

This is less an ambiguity in the rule than a warning about reading it: a layout a person reads at a glance is easy to
lose, and a policy built from the lost version is consistent, quotes the rule word for word, and is still wrong. Two
readings compared by machine find that; a check of either alone does not.

## What this is not

- Two documents, not a survey. Each comparison needs a second, careful reading, which takes a person's time.
- The readings are ours. A plan's clinical staff, or the contractor, may know the intended reading at once.
- No real patient's record was used: a "record" here is a combination of documented facts, every one considered.
