import { useState, type FormEvent } from 'react'
import { useDispatch } from 'react-redux'
import { Cite, decisionText, errorText, Result, when } from './common'
import { WhatIf } from './WhatIf'
import { CaseReceipts } from './Receipt'
import { LetterView } from './LetterView'
import { ReceiveAppeal, WorkPanel } from './Work'
import { IconCases, IconCheck, IconClock, IconCross } from './Icons'
import {
  go,
  useAppealMutation,
  useCheckAutomaticMutation,
  useExtendMutation,
  useCaseQuery,
  useCasesQuery,
  useConfirmMutation,
  useDocumentsQuery,
  useFetchDocumentsMutation,
  useReadMutation,
  useReviewMutation,
} from './store'
import { can, type CaseDetail, type CaseStatus, type CaseSummary, type Exemption, type ExtensionReason, type Me, type Precedence } from './types'

const statusText: Record<CaseStatus, string> = {
  pended: 'Waiting for review',
  decided: 'Decided by policy',
  reviewed: 'Reviewed',
  superseded: 'Superseded',
}

const filters: { value: CaseStatus | ''; label: string }[] = [
  { value: 'pended', label: 'Waiting' },
  { value: 'reviewed', label: 'Reviewed' },
  { value: 'decided', label: 'Decided by policy' },
  { value: '', label: 'All' },
]

/** A system|code or system|value pair, without the system's URL in the way. */
function code(s: string): string {
  const i = s.lastIndexOf('|')
  return i < 0 ? s : s.slice(i + 1)
}

export function Cases() {
  const [status, setStatus] = useState<CaseStatus | ''>('pended')
  // New cases arrive from the PAS server, not from this browser, so a cached list is stale by nature: fetch it on every
  // visit and every change of filter, and every 15 seconds while it is open.
  const { data, error, isFetching, refetch } = useCasesQuery(status, {
    pollingInterval: 15000,
    refetchOnMountOrArgChange: true,
    refetchOnFocus: true,
  })
  const dispatch = useDispatch()
  return (
    <section aria-labelledby="cases-h">
      <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 id="cases-h" className="m-0">
            Cases
          </h2>
          <p className="meta m-0">Every request the PAS server sent, what the engine decided, and what waits for a person.</p>
        </div>
      </div>
      <Stats />
      <div className="filters" role="group" aria-label="Show">
        {filters.map((f) => (
          <button
            key={f.value}
            type="button"
            className={status === f.value ? 'chip on' : 'chip'}
            aria-pressed={status === f.value}
            onClick={() => setStatus(f.value)}
          >
            {f.label}
          </button>
        ))}
        <button type="button" className="link" onClick={() => refetch()} disabled={isFetching}>
          {isFetching ? 'Refreshing…' : 'Refresh'}
        </button>
      </div>
      {error && <p role="alert">{errorText(error, 'Could not load cases.')}</p>}
      {data && data.length === 0 && <p>{status === 'pended' ? 'Nothing is waiting for review.' : 'No cases.'}</p>}
      {data && data.length > 0 && (
        <table className="cases" aria-busy={isFetching}>
          <thead>
            <tr>
              <th scope="col">Received</th>
              <th scope="col">Policy</th>
              <th scope="col">Service</th>
              <th scope="col">Claim</th>
              <th scope="col">Engine</th>
              <th scope="col">Status</th>
              <th scope="col">Due</th>
            </tr>
          </thead>
          <tbody>
            {data.map((c) => (
              <Row key={c.id} c={c} open={() => dispatch(go({ screen: 'case', id: c.id }))} />
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}

/** The queue at a glance: what waits, what is due soon, and what the policies settled on their own. */
function Stats() {
  const { data } = useCasesQuery('', { pollingInterval: 15000, refetchOnMountOrArgChange: true })
  if (!data) return null
  const now = Date.now()
  const waiting = data.filter((c) => c.status === 'pended')
  const soon = waiting.filter((c) => new Date(c.due).getTime() - now < 24 * 36e5)
  const overdue = waiting.filter((c) => new Date(c.due).getTime() < now)
  const auto = data.filter((c) => c.status === 'decided')
  const approved = data.filter((c) => (c.review ?? c.decision) === 'approve' && c.status !== 'pended')
  const denied = data.filter((c) => (c.review ?? c.decision) === 'deny' && c.status !== 'pended')
  const cards = [
    {
      label: 'Waiting for review',
      value: waiting.length,
      note: waiting.length ? 'a person decides these' : 'queue is clear',
      icon: IconCases,
      tone: 'from-amber-400 to-orange-500',
      text: 'text-due',
    },
    {
      label: 'Due within a day',
      value: soon.length,
      note: overdue.length ? `${overdue.length} overdue` : 'none overdue',
      icon: IconClock,
      tone: overdue.length ? 'from-rose-500 to-red-600' : 'from-sky-400 to-blue-600',
      text: overdue.length ? 'text-bad' : 'text-info',
    },
    {
      label: 'Approved',
      value: approved.length,
      note: `${auto.length} settled by policy alone`,
      icon: IconCheck,
      tone: 'from-emerald-400 to-teal-600',
      text: 'text-ok',
    },
    {
      label: 'Denied',
      value: denied.length,
      note: 'each with its proof',
      icon: IconCross,
      tone: 'from-rose-400 to-fuchsia-600',
      text: 'text-bad',
    },
  ]
  return (
    <div className="mb-5 grid gap-3 sm:grid-cols-2 lg:grid-cols-4" aria-label="The queue at a glance">
      {cards.map((k) => {
        const I = k.icon
        return (
          <div key={k.label} className="rounded-xl border border-line bg-raised p-4 shadow-sm">
            <div className="flex items-start justify-between">
              <span className="text-[11px] font-bold uppercase tracking-[0.1em] text-faint">{k.label}</span>
              <span className={`grid h-8 w-8 place-items-center rounded-lg bg-gradient-to-br ${k.tone} text-white shadow`}>
                <I size={16} />
              </span>
            </div>
            <div className={`mt-1 text-3xl font-extrabold tabular-nums ${k.text}`}>{k.value}</div>
            <div className="meta text-xs">{k.note}</div>
          </div>
        )
      })}
    </div>
  )
}

function Row({ c, open }: { c: CaseSummary; open: () => void }) {
  return (
    <tr>
      <td>
        <a
          href={`#/case/${c.id}`}
          onClick={(e) => {
            e.preventDefault()
            open()
          }}
        >
          {when(c.created)}
        </a>
      </td>
      <td>{c.policyTitle || c.policy}</td>
      <td>
        {code(c.service)} <span className="meta">item {c.item}</span>
      </td>
      <td>{code(c.claim)}</td>
      <td>
        <span className={`badge ${c.decision}`}>{decisionText[c.decision]}</span>
        {c.goldCard && <span className="meta"> · gold card</span>}
        {c.proposed && <span className="warn"> · denial proposed</span>}
        {c.expedited && <span className="meta"> · expedited</span>}
      </td>
      <td>
        {statusText[c.status]}
        {c.review && ` (${decisionText[c.review].toLowerCase()})`}
        {c.delivery && !c.delivery.ok && <span className="warn"> · not delivered</span>}
      </td>
      <td>{c.status === 'pended' ? <Due due={c.due} extended={c.extended} /> : <span className="meta">—</span>}</td>
    </tr>
  )
}

/** When a waiting decision is due, in words a queue is worked by: overdue in red, within a day in amber. */
export function Due({ due, extended, now = Date.now() }: { due: string; extended?: boolean; now?: number }) {
  const ms = new Date(due).getTime() - now
  const hours = Math.round(Math.abs(ms) / 36e5)
  const span = hours < 48 ? `${hours} h` : `${Math.round(hours / 24)} days`
  const cls = ms < 0 ? 'warn strong' : ms < 24 * 36e5 ? 'warn' : undefined
  return (
    <span className={cls} title={new Date(due).toLocaleString()}>
      {ms < 0 ? `overdue by ${span}` : `in ${span}`}
      {extended && <span className="meta"> · extended</span>}
    </span>
  )
}

export function CaseView({ id, me }: { id: string; me: Me }) {
  const { data: c, error, isLoading } = useCaseQuery(id)
  const dispatch = useDispatch()
  return (
    <section aria-labelledby="case-h">
      <p>
        <a
          href="#/cases"
          onClick={(e) => {
            e.preventDefault()
            dispatch(go({ screen: 'cases', id: null }))
          }}
        >
          ← Cases
        </a>
      </p>
      {isLoading && <p>Loading…</p>}
      {error && <p role="alert">{errorText(error, 'Could not load the case.')}</p>}
      {c && <CaseBody c={c} me={me} />}
    </section>
  )
}

function CaseBody({ c, me }: { c: CaseDetail; me: Me }) {
  const dispatch = useDispatch()
  return (
    <>
      <h2 id="case-h">
        {c.policyTitle} <span className="meta">item {c.item}</span>
      </h2>
      <dl className="facts">
        <dt>Status</dt>
        <dd>{statusText[c.status]}</dd>
        <dt>Received</dt>
        <dd>{when(c.created)}</dd>
        {c.status === 'pended' && (
          <>
            <dt>Due</dt>
            <dd>
              <Due due={c.due} extended={!!c.extension} /> <span className="meta">({when(c.due)})</span>
            </dd>
          </>
        )}
        <dt>Service</dt>
        <dd>{code(c.service)}</dd>
        <dt>Claim</dt>
        <dd>{c.claim}</dd>
        <dt>Patient</dt>
        <dd>{c.patient}</dd>
        <dt>Provider</dt>
        <dd>{c.provider}</dd>
        {c.eligibility && (
          <>
            <dt>Coverage</dt>
            <dd className={c.eligibility.status === 'active' ? 'text-emerald-400' : 'text-amber-400'}>
              {c.eligibility.why} <span className="meta">(member file {c.eligibility.source})</span>
            </dd>
          </>
        )}
        <dt>Request</dt>
        <dd>
          <a href={`/api/cases/${encodeURIComponent(c.id)}/bundle`} target="_blank" rel="noreferrer">
            The PAS request as received
          </a>{' '}
          <span className="meta">sha256 {c.bundle.slice(0, 12)}…</span>
        </dd>
      </dl>
      {c.supersededBy && (
        <p className="note">
          A later request for the same service replaced this one:{' '}
          <a
            href={`#/case/${c.supersededBy}`}
            onClick={(e) => {
              e.preventDefault()
              dispatch(go({ screen: 'case', id: c.supersededBy! }))
            }}
          >
            open it
          </a>
          .
        </p>
      )}

      {c.channel && (
        <p className="meta">
          Received by {c.channel} and keyed in by {c.keyedBy}: its answers are their reading of what arrived, which is kept
          with the request below. The provider is told by letter.
        </p>
      )}
      <h3>{c.channel ? 'The answer, for the letter' : 'What the provider was told'}</h3>
      <p>
        <span className={`badge ${c.answer.decision}`}>{decisionText[c.answer.decision]}</span> {c.answer.why}
      </p>
      {c.answer.requested && c.answer.requested.length > 0 && (
        <ul>
          {c.answer.requested.map((r) => (
            <li key={r}>{r}</li>
          ))}
        </ul>
      )}
      {(c.answer.questionnaire || (c.answer.attachments && c.answer.attachments.length > 0)) && (
        <p className="meta">
          Asked for through the PAS server's CDex request:
          {c.answer.questionnaire && <> the questionnaire <code>{c.answer.questionnaire}</code></>}
          {c.answer.attachments && c.answer.attachments.length > 0 && (
            <> {c.answer.questionnaire ? 'and ' : ''}documents LOINC {c.answer.attachments.join(', ')}</>
          )}
          .
        </p>
      )}

      {c.exemption && <ExemptionNote e={c.exemption} />}
      {c.precedence && <PrecedenceNote p={c.precedence} title={c.policyTitle} />}
      {c.eligibility && c.eligibility.status !== 'active' && (
        <p className="note warn">
          <strong>Pended on the member's coverage.</strong> The policy {c.result.outcome === 'approved' ? 'would approve' : c.result.outcome === 'denied' ? 'would deny' : 'decides'} this
          request, but the member file says: {c.eligibility.why}. Check the coverage with the plan's enrollment records before
          deciding; a file that is late or wrong is not a reason to deny.
        </p>
      )}
      {c.proposed && (
        <div className="note warn">
          <p>
            <strong>The engine proposes a denial.</strong> Here only a clinician denies on medical necessity, so it was not
            sent; the provider was told the request is with a clinician. What the denial would have said:
          </p>
          <blockquote className="cite">{c.proposed.why}</blockquote>
          <p className="meta">
            Weigh it against the evidence below, not instead of it: approve if the record supports the service.
          </p>
        </div>
      )}

      {c.review && (
        <article className={`result ${c.review.decision === 'approve' ? 'approved' : 'denied'}`}>
          <h3>
            <span className="tone-icon" aria-hidden="true">
              {c.review.decision === 'approve' ? <IconCheck size={20} strokeWidth={2.6} /> : <IconCross size={20} strokeWidth={2.6} />}
            </span>
            {c.review.automatic
              ? `${decisionText[c.review.decision]} on its documents, with no person`
              : `Reviewed: ${decisionText[c.review.decision]}, by ${c.review.by}`}
          </h3>
          <p>{c.review.reason}</p>
          {c.review.automatic && <AutomaticCheck c={c} me={me} />}
          <p className="meta">
            {when(c.review.time)}
            {c.review.reviewerNpi && ` · NPI ${c.review.reviewerNpi}`}
            {c.review.credential && ` · ${c.review.credential}`}
          </p>
          <Delivery c={c} />
        </article>
      )}
      <WorkPanel c={c} me={me} />
      {c.status === 'pended' && me.role !== 'viewer' && <ReviewForm c={c} me={me} />}
      {c.status === 'pended' && me.role === 'viewer' && <p className="note">A reviewer decides this case.</p>}

      {c.appeal && (
        <p className={c.appeal.outcome === 'overturned' ? 'note ok' : 'note'}>
          <strong>Appeal {c.appeal.outcome === 'overturned' ? 'overturned the denial' : 'upheld the denial'}</strong>
          {c.appeal.decided && ` on ${c.appeal.decided}`}: {c.appeal.reason}{' '}
          <span className="meta">(recorded by {c.appeal.by}, {when(c.appeal.time)})</span>
        </p>
      )}
      {!c.appeal && (c.status === 'reviewed' ? c.review?.decision : c.answer.decision) === 'deny' && c.status !== 'superseded' && (
        <ReceiveAppeal c={c} me={me} />
      )}
      {!c.appeal && can(me, 'reviewer', 'appeals') &&
        (c.status === 'reviewed' ? c.review?.decision : c.answer.decision) === 'deny' &&
        c.status !== 'superseded' && <AppealForm c={c} />}
      {c.extension && (
        <p className="note">
          <strong>Timeframe extended by {c.extension.days} days</strong> ({extensionText[c.extension.reason]}):{' '}
          {c.extension.note} <span className="meta">(by {c.extension.by}, {when(c.extension.time)}; now due {when(c.extension.due)})</span>
        </p>
      )}
      {!c.extension && c.status === 'pended' && !c.drug && me.role !== 'viewer' && <ExtendForm c={c} />}

      {c.redecided && <Redecided c={c} />}
      {c.redecideError && <p className="note warn">Could not decide it again: {c.redecideError}</p>}
      <Documents c={c} me={me} />

      <Evidence c={c} />
      <DecisionTabs c={c} />
      <LetterView id={c.id} />
      <CaseReceipts c={c} />
      <History c={c} />
    </>
  )
}

/** The after-the-fact check of an automatic approval: does the document show what the approval rests on? */
function AutomaticCheck({ c, me }: { c: CaseDetail; me: Me }) {
  const [check, checking] = useCheckAutomaticMutation()
  const [note, setNote] = useState('')
  if (c.qa)
    return (
      <p className={c.qa.agree ? 'meta' : 'warn'}>
        Checked by {c.qa.by} {when(c.qa.time)}: {c.qa.agree ? 'the documents show it.' : `disagreed: ${c.qa.note}`}
      </p>
    )
  if (me.role === 'viewer') return c.review?.sampled ? <p className="meta">Set aside for a reviewer to check.</p> : null
  return (
    <div className="note">
      <p>
        {c.review?.sampled ? 'Set aside for a check. ' : ''}Read each quote against the document below: does it show what the
        approval rests on? The approval stands either way; a disagreement is counted in the Reports.
      </p>
      <input aria-label="What the documents do not show" value={note} onChange={(e) => setNote(e.target.value)}
        placeholder="If not: what the documents do not show" />{' '}
      <button type="button" disabled={checking.isLoading} onClick={() => check({ id: c.id, agree: true, note: '' })}>
        The documents show it
      </button>{' '}
      <button type="button" className="danger" disabled={checking.isLoading || !note.trim()}
        onClick={() => check({ id: c.id, agree: false, note })}>
        They do not
      </button>
      {checking.error && <p role="alert">{errorText(checking.error, 'Not recorded.')}</p>}
    </div>
  )
}

function Delivery({ c }: { c: CaseDetail }) {
  if (c.channel)
    return <p className="meta">Received by {c.channel}: no PAS server waits for this review. Send the provider its letter.</p>
  if (!c.request) return <p className="meta">The PAS server gave no request id, so this review stays here.</p>
  if (!c.delivery) return <p className="meta">Sending to the PAS server…</p>
  if (c.delivery.ok) return <p className="meta">Delivered to the PAS server {when(c.delivery.time)}.</p>
  return (
    <p role="alert" className="warn">
      {c.delivery.final ? 'The PAS server refused it: ' : 'Not delivered yet; retrying: '}
      {c.delivery.error}
    </p>
  )
}

function Redecided({ c }: { c: CaseDetail }) {
  const r = c.redecided!
  const what = [
    r.received > 0 && `${r.received} delivery of documents sent since`,
    r.confirmed > 0 && `${r.confirmed} finding(s) a reviewer confirmed`,
  ]
    .filter(Boolean)
    .join(' and ')
  return (
    <div className="note">
      <p>
        <strong>On the request as it now stands</strong> ({what}), policy v{r.policyVersion} would:{' '}
        <span className={`badge ${r.result.outcome === 'approved' ? 'approve' : r.result.outcome === 'denied' ? 'deny' : 'pend'}`}>
          {r.result.outcome}
        </span>
        . This decides nothing by itself: it is for the reviewer to weigh.
      </p>
      <details>
        <summary>Its proof</summary>
        <Result result={r.result} heading={3} />
      </details>
    </div>
  )
}

function Documents({ c, me }: { c: CaseDetail; me: Me }) {
  const { data } = useDocumentsQuery(c.id)
  const [read, reading] = useReadMutation()
  const [confirm, confirming] = useConfirmMutation()
  const [fetchDocs, fetching] = useFetchDocumentsMutation()
  const decided = new Map((c.confirmations ?? []).map((cf) => [`${cf.reading}/${cf.index}`, cf]))
  const reviewer = me.role !== 'viewer'
  if (!data) return null
  const readings = c.readings ?? []
  if (data.documents.length === 0 && readings.length === 0 && !data.fetch) return null
  return (
    <section aria-labelledby="docs-h">
      <h3 id="docs-h">Documents</h3>
      {data.documents.length > 0 && (
        <ul>
          {data.documents.map((d) => (
            <li key={d.reference}>
              {d.what || 'A document'}{' '}
              {d.truncated && <span className="meta">(long: only its start would be read) </span>}
              {d.extractedBy && (
                <span className="meta">
                  ({d.contentType}, its text made by {d.extractedBy}: confirm quotes against{' '}
                  <a href={`/api/cases/${encodeURIComponent(c.id)}/documents/original?ref=${encodeURIComponent(d.reference)}`}>
                    the original
                  </a>
                  ){' '}
                </span>
              )}
              {reviewer && data.reader && (
                <button
                  type="button"
                  className="link"
                  disabled={reading.isLoading}
                  onClick={() => read({ id: c.id, document: d.reference })}
                >
                  {reading.isLoading && reading.originalArgs?.document === d.reference ? 'Reading…' : 'Read it with the model'}
                </button>
              )}
              <cite>{d.reference}</cite>
            </li>
          ))}
        </ul>
      )}
      {data.skipped.length > 0 && <p className="meta">Not readable here: {data.skipped.join('; ')}.</p>}
      {reading.error && <p role="alert">{errorText(reading.error, 'The model did not read it.')}</p>}
      {reviewer && data.fetch && (
        <p>
          <button type="button" className="link" disabled={fetching.isLoading} onClick={() => fetchDocs(c.id)}>
            {fetching.isLoading ? 'Asking…' : 'Fetch documents the provider sent since'}
          </button>{' '}
          {fetching.data && <span className="meta">{fetching.data.received} received.</span>}
          {fetching.error && <span role="alert">{errorText(fetching.error, 'Could not fetch them.')}</span>}
        </p>
      )}
      {readings.map((rd, ri) => (
        <div key={ri} className="reading">
          <p className="meta">
            {rd.model} read {rd.document}. Nothing it found is evidence until a reviewer confirms it against the document.
          </p>
          {rd.proposals.length === 0 && <p>It found nothing the policy asks about.</p>}
          <ul className="plain">
            {rd.proposals.map((pr, pi) => {
              const cf = decided.get(`${ri}/${pi}`)
              return (
                <li key={pi}>
                  <strong>{pr.text}</strong>: {pr.finding}
                  <blockquote className="cite">“{pr.quote}”</blockquote>
                  {cf ? (
                    <span className={cf.confirmed ? 'badge approve' : 'badge'}>
                      {cf.confirmed ? 'Confirmed' : 'Rejected'} by {cf.by}
                    </span>
                  ) : (
                    reviewer && (
                      <>
                        <button
                          type="button"
                          disabled={confirming.isLoading}
                          onClick={() => confirm({ id: c.id, reading: ri, index: pi, confirmed: true })}
                        >
                          Confirm
                        </button>{' '}
                        <button
                          type="button"
                          className="link"
                          disabled={confirming.isLoading}
                          onClick={() => confirm({ id: c.id, reading: ri, index: pi, confirmed: false })}
                        >
                          Reject
                        </button>
                      </>
                    )
                  )}
                </li>
              )
            })}
          </ul>
          {rd.refused.length > 0 && (
            <details>
              <summary>{rd.refused.length} answer(s) refused</summary>
              <ul>
                {rd.refused.map((r, i) => (
                  <li key={i} className="meta">
                    {r}
                  </li>
                ))}
              </ul>
            </details>
          )}
        </div>
      ))}
      {confirming.error && <p role="alert">{errorText(confirming.error, 'Not recorded.')}</p>}
    </section>
  )
}

const extensionText: Record<ExtensionReason, string> = {
  requested: 'the enrollee asked for it',
  evidence: 'more medical evidence is needed that may change a denial',
  exceptional: 'extraordinary or non-routine circumstances',
}

/** Extends a waiting case's decision timeframe, for one of the reasons the rules allow. The enrollee is told why. */
function ExtendForm({ c }: { c: CaseDetail }) {
  const [days, setDays] = useState(c.maxExtensionDays)
  const [reason, setReason] = useState<ExtensionReason | ''>('')
  const [note, setNote] = useState('')
  const [extend, { error, isLoading }] = useExtendMutation()
  const overdue = new Date(c.due).getTime() <= Date.now()
  return (
    <details className="review">
      <summary>Extend the decision timeframe</summary>
      {overdue ? (
        <p className="meta">The decision was due {when(c.due)}: a timeframe is extended before it runs out, not after.</p>
      ) : (
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (reason) extend({ id: c.id, days, reason, note: note.trim() })
          }}
        >
          <div className="findings" role="radiogroup" aria-label="Reason for the extension">
            {(Object.keys(extensionText) as ExtensionReason[]).map((r) => (
              <label key={r}>
                <input type="radio" name="extend" checked={reason === r} onChange={() => setReason(r)} required />
                {extensionText[r][0].toUpperCase() + extensionText[r].slice(1)}
              </label>
            ))}
          </div>
          <label htmlFor="extend-days">Days (at most {c.maxExtensionDays})</label>
          <input
            id="extend-days"
            type="number"
            min={1}
            max={c.maxExtensionDays}
            value={days}
            onChange={(e) => setDays(Number(e.target.value))}
          />
          <label htmlFor="extend-note">What the enrollee is told the delay is for</label>
          <textarea id="extend-note" required value={note} onChange={(e) => setNote(e.target.value)} />
          <button type="submit" disabled={isLoading || !reason || !note.trim() || days < 1 || days > c.maxExtensionDays}>
            Extend
          </button>
          {error && <p role="alert">{errorText(error, 'Not extended.')}</p>}
        </form>
      )}
    </details>
  )
}

function AppealForm({ c }: { c: CaseDetail }) {
  const [outcome, setOutcome] = useState<'upheld' | 'overturned' | ''>('')
  const [reason, setReason] = useState('')
  const [decided, setDecided] = useState('')
  const [appeal, { error, isLoading }] = useAppealMutation()
  return (
    <details className="review">
      <summary>{c.appealTaken ? 'Decide the appeal' : 'Record the outcome of an appeal decided elsewhere'}</summary>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (outcome) appeal({ id: c.id, outcome, reason: reason.trim(), decided })
        }}
      >
        <div className="findings" role="radiogroup" aria-label="Appeal outcome">
          {(['upheld', 'overturned'] as const).map((o) => (
            <label key={o}>
              <input type="radio" name="appeal" checked={outcome === o} onChange={() => setOutcome(o)} required />
              {o === 'upheld' ? 'Upheld (the denial stands)' : 'Overturned (approved on appeal)'}
            </label>
          ))}
        </div>
        <label htmlFor="appeal-reason">Reason</label>
        <textarea id="appeal-reason" required value={reason} onChange={(e) => setReason(e.target.value)} />
        <label htmlFor="appeal-date">Decided on (optional)</label>
        <input id="appeal-date" type="date" value={decided} onChange={(e) => setDecided(e.target.value)} />
        <button type="submit" disabled={isLoading || !outcome || !reason.trim()}>
          Record
        </button>
        {error && <p role="alert">{errorText(error, 'Not recorded.')}</p>}
      </form>
    </details>
  )
}

/** Why this policy decided when other policies are for the same service. */
function PrecedenceNote({ p, title }: { p: Precedence; title: string }) {
  return (
    <div className={p.unsettled && p.unsettled.length > 0 ? 'note warn' : 'note'}>
      <p>
        <strong>Decided under {title}</strong>: {p.why}.
      </p>
      <ul>
        {p.unsettled?.map((a) => (
          <li key={a.policy}>
            May also fall under <strong>{a.title}</strong> v{a.version}: {a.why}. Under it, this policy may approve but not
            deny.
          </li>
        ))}
        {p.setAside?.map((a) => (
          <li key={a.policy}>
            Not <strong>{a.title}</strong> v{a.version}: {a.why}.
          </li>
        ))}
      </ul>
    </div>
  )
}

function ExemptionNote({ e }: { e: Exemption }) {
  return (
    <div className="note ok">
      <p>
        <strong>Approved under {e.program}</strong>
        {e.basis && ` (${e.basis})`}: provider NPI {e.npi} had {e.approved} of {e.decisions} decided requests approved in
        the {e.lookBackDays} days before this one ({Math.round((100 * e.approved) / e.decisions)}%;{' '}
        {Math.round(e.minRate * 100)}% of at least {e.minDecisions} required). The engine's own result is below.
      </p>
      <details>
        <summary>The {e.counted.length} cases counted</summary>
        <ul className="plain">
          {e.counted.map((id) => (
            <li key={id}>
              <a href={`#/case/${id}`}>{id}</a>
            </li>
          ))}
        </ul>
      </details>
    </div>
  )
}

function ReviewForm({ c, me }: { c: CaseDetail; me: Me }) {
  const [decision, setDecision] = useState<'approve' | 'deny' | ''>('')
  const [reason, setReason] = useState('')
  const [npi, setNpi] = useState('')
  const [review, { error, isLoading }] = useReviewMutation()
  const clinician = Boolean(me.npi && me.credential)
  const mayDeny = clinician || !me.clinicianDenials

  function submit(e: FormEvent) {
    e.preventDefault()
    if (decision) review({ id: c.id, decision, reason: reason.trim(), reviewerNpi: npi.trim() })
  }

  return (
    <form className="review" onSubmit={submit} aria-labelledby="review-h">
      <h3 id="review-h">Your decision</h3>
      <div className="findings" role="radiogroup" aria-label="Decision">
        {(['approve', 'deny'] as const).map((d) => (
          <label key={d}>
            <input
              type="radio"
              name="decision"
              checked={decision === d}
              onChange={() => setDecision(d)}
              disabled={d === 'deny' && !mayDeny}
              required
            />
            {decisionText[d]}
          </label>
        ))}
      </div>
      {!mayDeny && (
        <p className="meta">
          Only a clinician may deny here. You can approve, or leave the case for a clinician; an administrator records a
          clinician's NPI and credential with <code>vaglio user clinician</code>.
        </p>
      )}
      <label htmlFor="reason">Reason{decision === 'deny' ? ' (sent to the provider; name the clause and the finding)' : ''}</label>
      <textarea id="reason" required value={reason} onChange={(e) => setReason(e.target.value)} />
      {clinician ? (
        <p className="meta">
          Recorded with your NPI {me.npi} and credential {me.credential}.
        </p>
      ) : (
        <>
          <label htmlFor="npi">Your NPI (optional)</label>
          <input id="npi" inputMode="numeric" pattern="[0-9]{10}" value={npi} onChange={(e) => setNpi(e.target.value)} />
        </>
      )}
      <button type="submit" disabled={isLoading || !decision || !reason.trim()}>
        {isLoading ? 'Recording…' : 'Record decision'}
      </button>
      {error && <p role="alert">{errorText(error, 'The decision was not recorded.')}</p>}
    </form>
  )
}

function Evidence({ c }: { c: CaseDetail }) {
  const x = c.evidence
  return (
    <>
      <h3>Evidence found in the request</h3>
      {x.findings.length === 0 && <p>No source of this policy found anything in the request.</p>}
      {x.findings.length > 0 && (
        <ul>
          {x.findings.map((f, i) => (
            <li key={i}>
              <strong>{f.argument}</strong>: documented {f.finding}
              <Cite c={f} />
            </li>
          ))}
        </ul>
      )}
      {x.contradictions.length > 0 && (
        <>
          <h3>Contradicted</h3>
          <ul>
            {x.contradictions.map((k) => (
              <li key={k.argument}>
                <strong>{k.argument}</strong>: documented present in {k.present.map((f) => f.reference).join(', ')} and absent
                in {k.absent.map((f) => f.reference).join(', ')}
              </li>
            ))}
          </ul>
        </>
      )}
      {x.unread.length > 0 && (
        <>
          <h3>In the request but not read by any rule</h3>
          <ul>
            {x.unread.map((u) => (
              <li key={u.reference}>
                {u.what} <cite>{u.reference}</cite>
              </li>
            ))}
          </ul>
        </>
      )}
      {x.ignored.length > 0 && (
        <>
          <h3>Could not be read</h3>
          <ul>
            {x.ignored.map((s) => (
              <li key={s}>{s}</li>
            ))}
          </ul>
        </>
      )}
    </>
  )
}

const actionText: Record<string, string> = {
  'case.opened': 'Decided by the engine',
  'case.viewed': 'Viewed',
  'case.read': 'A document read by the model',
  'case.confirmed': 'A proposed finding decided',
  'case.received': 'Documents received from the PAS server',
  'case.appealed': 'Appeal outcome recorded',
  'case.appeal-received': 'Appeal received',
  'case.assigned': 'Assigned',
  'case.escalated': 'Sent to a physician',
  'case.peer-to-peer': 'Peer-to-peer call',
  'case.extended': 'Timeframe extended',
  'case.qa': 'Quality check',
  'case.reviewed': 'Reviewed',
  'case.superseded': 'Superseded',
  'case.delivered': 'Delivery attempt',
}

function History({ c }: { c: CaseDetail }) {
  return (
    <>
      <h3>History</h3>
      <ol className="history">
        {c.history.map((r) => (
          <li key={r.seq}>
            <span className="meta">{when(r.time)}</span> {actionText[r.action] ?? r.action}{' '}
            <span className="meta">
              by {r.actor} · #{r.seq}
            </span>
          </li>
        ))}
      </ol>
    </>
  )
}

/** The decision as made, and beside it the what-if sandbox over the same evidence. */
function DecisionTabs({ c }: { c: CaseDetail }) {
  const [tab, setTab] = useState<'decision' | 'whatif'>('decision')
  const tabs = [
    { id: 'decision', label: 'The decision' },
    { id: 'whatif', label: 'What if…' },
  ] as const
  return (
    <div className="mt-4">
      <div role="tablist" aria-label="Decision" className="mb-3 flex gap-2">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            id={`tab-${t.id}`}
            aria-selected={tab === t.id}
            aria-controls={`panel-${t.id}`}
            className={`chip ${tab === t.id ? 'on' : ''}`}
            onClick={() => setTab(t.id)}
            onKeyDown={(e) => {
              if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
                const next = tab === 'decision' ? 'whatif' : 'decision'
                setTab(next)
                document.getElementById(`tab-${next}`)?.focus()
              }
            }}
            tabIndex={tab === t.id ? 0 : -1}
          >
            {t.label}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`}>
        {tab === 'decision' ? <Result result={c.result} heading={3} /> : <WhatIf c={c} original={c.result} />}
      </div>
    </div>
  )
}
