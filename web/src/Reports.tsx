import { useState } from 'react'
import { errorText } from './common'
import {
  useGoldCardCountingMutation,
  useGoldCardQuery,
  useMeQuery,
  useMetricsQuery,
  useAutomaticQuery,
  useFairnessQuery,
  useListReviewQuery,
  useOverridesQuery,
  usePendsQuery,
  type Period,
} from './store'
import type { MetricsGroup, Metrics as MetricsReport } from './types'

function num(v: number | null | undefined, digits: number, suffix = ''): string {
  return v == null ? 'n/a' : v.toFixed(digits) + suffix
}

const rows: { label: string; value: (g: MetricsGroup) => string }[] = [
  { label: 'Requests', value: (g) => String(g.requests) },
  { label: 'Approved', value: (g) => `${g.approved} (${num(g.approvedPercent, 1, '%')})` },
  { label: 'Denied', value: (g) => `${g.denied} (${num(g.deniedPercent, 1, '%')})` },
  { label: 'Awaiting determination', value: (g) => String(g.pending) },
  { label: 'Average time to determination', value: (g) => num(g.averageHours, 2, ' h') },
  { label: 'Median time to determination', value: (g) => num(g.medianHours, 2, ' h') },
  { label: 'Determined as received', value: (g) => String(g.realTime) },
  { label: 'Determined by a reviewer', value: (g) => String(g.afterReview) },
  { label: 'Approved under a gold card exemption', value: (g) => String(g.goldCard) },
  { label: 'Denials appealed (outcome recorded)', value: (g) => String(g.appealed) },
  {
    label: 'Approved after appeal',
    value: (g) => `${g.approvedAfterAppeal} (${num(g.approvedAfterAppealPercent, 1, '%')} of appealed)`,
  },
  { label: 'Timeframe extended', value: (g) => String(g.extended) },
  {
    label: 'Approved after an extension',
    value: (g) => `${g.approvedAfterExtension} (${num(g.approvedAfterExtensionPercent, 1, '%')} of determined)`,
  },
  { label: 'Determined late, or waiting past the deadline', value: (g) => String(g.late) },
]

export function Reports() {
  const [p, setP] = useState<Period>({ from: '', to: '' })
  const [shown, setShown] = useState<Period>(p)
  const csv = `/api/reports/metrics?${new URLSearchParams({ ...Object.fromEntries(Object.entries(shown).filter(([, v]) => v)), format: 'csv' })}`
  return (
    <section aria-labelledby="reports-h">
      <h2 id="reports-h">Reports</h2>
      <form
        className="period"
        onSubmit={(e) => {
          e.preventDefault()
          setShown(p)
        }}
      >
        <label htmlFor="from">From</label>
        <input id="from" type="date" value={p.from} onChange={(e) => setP({ ...p, from: e.target.value })} />
        <label htmlFor="to">To</label>
        <input id="to" type="date" value={p.to} onChange={(e) => setP({ ...p, to: e.target.value })} />
        <button type="submit">Show</button>
      </form>
      <Metrics period={shown} csv={csv} />
      <Reversals period={shown} />
      <ListReviewTable period={shown} />
      <Fairness period={shown} />
      <Automatic />
      <PendCauses period={shown} />
      <GoldCard />
    </section>
  )
}

function Metrics({ period, csv }: { period: Period; csv: string }) {
  const { data: m, error } = useMetricsQuery(period, { refetchOnMountOrArgChange: true })
  if (error) return <p role="alert">{errorText(error, 'Could not load the metrics.')}</p>
  if (!m) return <p>Loading…</p>
  return (
    <>
      <h3>
        Prior authorization metrics, {m.from} to {m.to}
      </h3>
      <p className="meta">
        As CMS-0057-F asks payers to publish them, with the counts CMS-0062-P proposes. <a href={csv}>Download as CSV</a>
      </p>
      <Glance m={m} />
      <table className="cases">
        <thead>
          <tr>
            <th scope="col">Metric</th>
            <th scope="col">Standard</th>
            <th scope="col">Expedited</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.label}>
              <th scope="row">{r.label}</th>
              <td>{r.value(m.standard)}</td>
              <td>{r.value(m.expedited)}</td>
            </tr>
          ))}
          {m.notTracked.map((n) => (
            <tr key={n}>
              <th scope="row">{n.split(':')[0]}</th>
              <td colSpan={2} className="meta">
                Not tracked: {n.split(':').slice(1).join(':').trim()}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {(m.drugStandard.requests > 0 || m.drugExpedited.requests > 0) && (
        <table className="cases">
          <caption>Drugs, counted apart as CMS does</caption>
          <thead>
            <tr>
              <th scope="col">Metric</th>
              <th scope="col">Standard</th>
              <th scope="col">Expedited</th>
            </tr>
          </thead>
          <tbody>
            {rows.slice(0, 6).map((r) => (
              <tr key={r.label}>
                <th scope="row">{r.label}</th>
                <td>{r.value(m.drugStandard)}</td>
                <td>{r.value(m.drugExpedited)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {m.superseded > 0 && (
        <p className="meta">{m.superseded} request(s) replaced by a resubmission before a decision are not counted.</p>
      )}
      {m.services.length > 0 && (
        <table className="cases">
          <caption>By service</caption>
          <thead>
            <tr>
              <th scope="col">Policy</th>
              <th scope="col">Service</th>
              <th scope="col">Requests</th>
              <th scope="col">Approved</th>
              <th scope="col">Denied</th>
              <th scope="col">Waiting</th>
            </tr>
          </thead>
          <tbody>
            {m.services.map((s) => (
              <tr key={s.policy + s.service}>
                <td>{s.title || s.policy}</td>
                <td>{s.service.slice(s.service.lastIndexOf('|') + 1)}</td>
                <td>{s.requests}</td>
                <td>{s.approved}</td>
                <td>{s.denied}</td>
                <td>{s.pending}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  )
}

function Reversals({ period }: { period: Period }) {
  const [by, setBy] = useState<'clause' | 'provider' | 'state'>('clause')
  const { data: o, error } = useOverridesQuery({ ...period, by }, { refetchOnMountOrArgChange: true })
  return (
    <>
      <h3>Denials that did not stand</h3>
      <p className="meta">
        The engine's denials (issued, or proposed to a clinician) that a clinician approved instead, or an appeal overturned.
        A clause reversed often is the first place to look for a policy that is wrong.
      </p>
      <div className="filters" role="group" aria-label="Group by">
        {(['clause', 'provider', 'state'] as const).map((b) => (
          <button key={b} type="button" className={by === b ? 'chip on' : 'chip'} aria-pressed={by === b} onClick={() => setBy(b)}>
            By {b}
          </button>
        ))}
      </div>
      {error && <p role="alert">{errorText(error, 'Could not load the reversals.')}</p>}
      {o && o.rows.length === 0 && <p>No denials in this period.</p>}
      {o && o.rows.length > 0 && (
        <table className="cases">
          <thead>
            <tr>
              <th scope="col">{by === 'clause' ? 'Clause' : by === 'provider' ? 'Provider' : 'State'}</th>
              <th scope="col">Denials</th>
              <th scope="col">Clinician approved</th>
              <th scope="col">Appealed</th>
              <th scope="col">Overturned</th>
              <th scope="col">Reversed</th>
            </tr>
          </thead>
          <tbody>
            {o.rows.map((r) => (
              <tr key={r.key}>
                <td>
                  {r.label}
                  {r.policy && <span className="meta"> ({r.policy})</span>}
                </td>
                <td>{r.denials}</td>
                <td>{r.overridden}</td>
                <td>{r.appealed}</td>
                <td>{r.overturned}</td>
                <td className={r.reversalPercent >= 25 ? 'warn' : undefined}>{r.reversalPercent.toFixed(0)}%</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  )
}

const verdictText: Record<string, string> = {
  'consider-removing': 'Consider removing',
  keep: 'Keep',
  'not-enough': 'Not enough requests',
}
const verdictClass: Record<string, string> = {
  'consider-removing': 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30',
  keep: 'bg-sky-500/10 text-sky-300 ring-sky-500/30',
  'not-enough': 'text-muted ring-line',
}

/** Which services review rarely changes: candidates to take off the prior authorization list. */
function ListReviewTable({ period }: { period: Period }) {
  const { data: l, error } = useListReviewQuery(period, { refetchOnMountOrArgChange: true })
  if (error) return <p role="alert">{errorText(error, 'Could not load the list review.')}</p>
  if (!l) return null
  const q = new URLSearchParams({ ...Object.fromEntries(Object.entries(period).filter(([, v]) => v)), format: 'csv' })
  return (
    <>
      <h3>Which services still need prior authorization</h3>
      <p className="meta">
        Per service, how often requests were approved in the end and how often a denial did not stand. A candidate to
        stop reviewing was approved at least {l.criteria.minApprovedPercent}% of the time over at least{' '}
        {l.criteria.minRequests} requests, with any denials reversed at least {l.criteria.minReversedPercent}% of the
        time. <a href={`/api/reports/list?${q}`}>Download as CSV</a>
      </p>
      {l.rows.length === 0 && <p>No requests in this period.</p>}
      {l.rows.length > 0 && (
        <table className="cases">
          <thead>
            <tr>
              <th scope="col">Service</th>
              <th scope="col">Requests</th>
              <th scope="col">Approved</th>
              <th scope="col">At once</th>
              <th scope="col">Denials reversed</th>
              <th scope="col">Median review</th>
              <th scope="col">Verdict</th>
            </tr>
          </thead>
          <tbody>
            {l.rows.map((r) => (
              <tr key={r.service}>
                <td>
                  <span className="font-mono">{r.code}</span> <span className="meta">{r.title}</span>
                </td>
                <td>{r.requests}</td>
                <td>{r.approvedPercent.toFixed(1)}%</td>
                <td>{r.engineApproved}</td>
                <td>{r.reversedPercent == null ? 'no denials' : `${r.reversed} of ${r.engineDenials}`}</td>
                <td>{r.medianReviewHours == null ? 'n/a' : `${r.medianReviewHours.toFixed(1)} h`}</td>
                <td>
                  <span className={`rounded-md px-2 py-0.5 text-xs ring-1 ${verdictClass[r.verdict]}`} title={r.why}>
                    {verdictText[r.verdict]}
                  </span>
                  <div className="meta text-xs">{r.why}</div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <p className="meta">{l.note}</p>
    </>
  )
}

/** Automatic reading and approval: what it did, and how the checks after the fact came out. */
function Automatic() {
  const { data: a, error } = useAutomaticQuery(undefined, { refetchOnMountOrArgChange: true })
  if (error) return <p role="alert">{errorText(error, 'Could not load automatic approvals.')}</p>
  if (!a || (!a.on && a.approved === 0)) return null
  return (
    <>
      <h3>Approvals on documents</h3>
      <p className="meta">
        {a.on
          ? a.approve
            ? `Documents are read as requests pend, and a request the policy approves on their quote-checked findings is approved with no person; ${a.samplePercent}% of those are set aside for a check. A denial or a pend always waits for a reviewer.`
            : 'Documents are read as requests pend; reviewers confirm the proposals.'
          : 'Automatic reading is off on this server; these approvals were made while it was on.'}
      </p>
      <dl className="grid grid-cols-2 gap-x-6 gap-y-1 sm:grid-cols-4">
        <dt className="meta">Approved on documents</dt>
        <dd>{a.approved}</dd>
        <dt className="meta">Checked</dt>
        <dd>
          {a.checked} of {a.sampled} set aside
        </dd>
        <dt className="meta">Reviewer disagreed</dt>
        <dd className={a.disagreePercent != null && a.disagreePercent > 5 ? 'warn' : undefined}>
          {a.disagree}
          {a.disagreePercent != null && ` (${a.disagreePercent.toFixed(0)}% of checked)`}
        </dd>
        {a.reader && (
          <>
            <dt className="meta">Read since start</dt>
            <dd>
              {a.reader.read} documents{a.reader.waiting ? `, ${a.reader.waiting} waiting` : ''}
              {a.reader.failed ? `, ${a.reader.failed} failed` : ''}
              {a.reader.dropped ? `, ${a.reader.dropped} left to reviewers` : ''}
            </dd>
          </>
        )}
      </dl>
      {a.toCheck.length > 0 && (
        <>
          <p>To check:</p>
          <ul className="plain">
            {a.toCheck.map((c) => (
              <li key={c.id}>
                <a href={`#/case/${c.id}`}>{c.id}</a> <span className="meta">{c.policy}</span>
              </li>
            ))}
          </ul>
        </>
      )}
      {a.disputed.length > 0 && (
        <>
          <p>Disagreed with:</p>
          <ul className="plain">
            {a.disputed.map((c) => (
              <li key={c.id}>
                <a href={`#/case/${c.id}`}>{c.id}</a> <span className="meta">{c.note}</span>
              </li>
            ))}
          </ul>
        </>
      )}
    </>
  )
}

const dimensionName: Record<string, string> = {
  sex: 'Sex',
  age: 'Age',
  race: 'Race',
  ethnicity: 'Ethnicity',
  state: 'State',
  line: 'Line of business',
}

/** Whether the engine's errors fall harder on some patients. Reads demographics from the stored requests: a reviewer's. */
function Fairness({ period }: { period: Period }) {
  const { data: me } = useMeQuery()
  const [open, setOpen] = useState(false)
  const allowed = me && (me.role !== 'viewer' || me.demo) // a demonstration's viewer may read it
  const { data: f, error, isFetching } = useFairnessQuery(period, { skip: !open || !allowed })
  if (!allowed) return null
  const pc = (v?: number) => (v == null ? 'n/a' : `${v.toFixed(0)}%`)
  return (
    <>
      <h3>Do errors fall harder on some patients?</h3>
      <p className="meta">
        Of the engine's denials, how many a clinician or an appeal reversed, and how often requests were pended to ask for
        more, by the patient's sex, age, race, ethnicity, state and line of business. Error, not approval: criteria treat
        patients differently on purpose. The report reads the demographics in each stored request, so making it is in the
        audit log.
      </p>
      {!open && (
        <button type="button" onClick={() => setOpen(true)}>
          Make the report
        </button>
      )}
      {open && isFetching && <p>Reading the requests…</p>}
      {error && <p role="alert">{errorText(error, 'Could not make the fairness report.')}</p>}
      {f && open && (
        <>
          <p>
            {f.requests} requests{f.unreadable ? `; ${f.unreadable} could not be read and are not counted` : ''}.
          </p>
          {f.dimensions.map((d) => (
            <table key={d.name} className="cases">
              <caption className="text-left font-semibold">{dimensionName[d.name] ?? d.name}</caption>
              <thead>
                <tr>
                  <th scope="col">Group</th>
                  <th scope="col">Requests</th>
                  <th scope="col">Denials reversed</th>
                  <th scope="col">Pended for more</th>
                  <th scope="col">Approved (context)</th>
                </tr>
              </thead>
              <tbody>
                {d.groups.map((g) => (
                  <tr key={g.value}>
                    <td>
                      {g.value}
                      {g.flags?.map((fl) => (
                        <div key={fl} className="text-xs text-amber-300">
                          ⚑ {fl}
                        </div>
                      ))}
                    </td>
                    {g.suppressed ? (
                      <td colSpan={4} className="meta">
                        Fewer than 11 requests: not shown
                      </td>
                    ) : (
                      <>
                        <td>{g.requests}</td>
                        <td>{g.engineDenials ? `${g.reversed} of ${g.engineDenials} (${pc(g.reversedPercent)})` : 'no denials'}</td>
                        <td>{pc(g.pendedPercent)}</td>
                        <td>{pc(g.approvedPercent)}</td>
                      </>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          ))}
          <p className="meta">{f.note}</p>
        </>
      )}
    </>
  )
}

function PendCauses({ period }: { period: Period }) {
  const { data: p, error } = usePendsQuery(period, { refetchOnMountOrArgChange: true })
  if (error) return <p role="alert">{errorText(error, 'Could not load the pend report.')}</p>
  if (!p) return null
  return (
    <>
      <h3>What pends requests most</h3>
      <p className="meta">
        Each is evidence a request lacked, so the provider was asked for it. One with a question is in the policy's DTR
        questionnaire: providers are not answering it. One without is found only in the record.
      </p>
      {p.missing.length === 0 && <p>No request was pended for information in this period.</p>}
      {p.missing.length > 0 && (
        <table className="cases">
          <thead>
            <tr>
              <th scope="col">Evidence</th>
              <th scope="col">Policy</th>
              <th scope="col">Pends</th>
              <th scope="col">Share</th>
              <th scope="col">Question</th>
            </tr>
          </thead>
          <tbody>
            {p.missing.map((c) => (
              <tr key={c.policy + c.argument}>
                <td>{c.text}</td>
                <td>{c.policy}</td>
                <td>{c.pends}</td>
                <td>{c.percent.toFixed(0)}%</td>
                <td>{c.question ? <code>{c.question}</code> : <span className="meta">record only</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {p.contradicted.length > 0 && (
        <>
          <h4>Contradicted in the record</h4>
          <ul>
            {p.contradicted.map((c) => (
              <li key={c.policy + c.argument}>
                {c.text} <span className="meta">({c.policy}): {c.pends} pend(s)</span>
              </li>
            ))}
          </ul>
        </>
      )}
    </>
  )
}

function GoldCard() {
  const { data: g, error } = useGoldCardQuery(undefined, { refetchOnMountOrArgChange: true })
  const { data: me } = useMeQuery()
  const [setCounting, counting] = useGoldCardCountingMutation()
  if (error) return <p role="alert">{errorText(error, 'Could not load gold card standings.')}</p>
  if (!g) return null
  return (
    <>
      <h3>Gold card</h3>
      {!g.enabled && (
        <p className="meta">No gold card programs are configured (vaglio serve -goldcard FILE).</p>
      )}
      {g.enabled && (
        <>
          <ul>
            {g.programs.map((p) => (
              <li key={p.name}>
                <strong>{p.name}</strong>
                {p.basis && ` (${p.basis})`}: {Math.round(p.minApprovalRate * 100)}% of at least {p.minDecisions} decided
                requests approved in {p.lookBackDays} days
                {p.states?.length ? `, patients in ${p.states.join(', ')}` : ''}
                {p.policies?.length ? `, for ${p.policies.join(', ')}` : ''}.
                {p.states && p.states.length > 0 && (
                  <div className="findings" role="radiogroup" aria-label={`Which decisions ${p.name} counts`}>
                    {(['all', 'states'] as const).map((mode) => (
                      <label key={mode}>
                        <input
                          type="radio"
                          name={`count-${p.name}`}
                          checked={(p.count ?? 'all') === mode}
                          disabled={me?.role !== 'admin' || counting.isLoading}
                          onChange={() => setCounting({ program: p.name, count: mode })}
                        />
                        {mode === 'all'
                          ? 'Count every decision for the policy, wherever the patient lives'
                          : `Count only decisions for patients in ${p.states!.join(', ')}`}
                      </label>
                    ))}
                  </div>
                )}
              </li>
            ))}
          </ul>
          {me?.role !== 'admin' && <p className="meta">An administrator chooses which decisions each program counts.</p>}
          {counting.error && <p role="alert">{errorText(counting.error, 'Not changed.')}</p>}
          {g.standings.length === 0 && <p>No provider with an NPI has a decided request yet.</p>}
          {g.standings.length > 0 && (
            <table className="cases">
              <thead>
                <tr>
                  <th scope="col">Program</th>
                  <th scope="col">NPI</th>
                  <th scope="col">Policy</th>
                  <th scope="col">Approved</th>
                  <th scope="col">Rate</th>
                  <th scope="col">Exempt</th>
                </tr>
              </thead>
              <tbody>
                {g.standings.map((s) => (
                  <tr key={s.program + s.npi + s.policy}>
                    <td>{s.program}</td>
                    <td>{s.npi}</td>
                    <td>{s.policy}</td>
                    <td>
                      {s.approved} of {s.decisions}
                    </td>
                    <td>{(s.rate * 100).toFixed(0)}%</td>
                    <td>{s.exempt ? <span className="badge approve">Exempt</span> : 'No'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}
    </>
  )
}

/** The window at a glance: how requests ended, drawn as a ring, and how fast they were decided. All requests, medical
 * and drug, standard and expedited; the table below has each count on its own. */
function Glance({ m }: { m: MetricsReport }) {
  const groups = [m.standard, m.expedited, m.drugStandard, m.drugExpedited]
  const sum = (f: (g: (typeof groups)[number]) => number) => groups.reduce((n, g) => n + f(g), 0)
  const approved = sum((g) => g.approved)
  const denied = sum((g) => g.denied)
  const pending = sum((g) => g.pending)
  const total = approved + denied + pending
  const realTime = sum((g) => g.realTime)
  const late = sum((g) => g.late)
  const median = m.standard.medianHours
  const segments = [
    { label: 'Approved', n: approved, color: 'var(--ok)' },
    { label: 'Denied', n: denied, color: 'var(--bad)' },
    { label: 'Waiting', n: pending, color: 'var(--due)' },
  ]
  const r = 52
  const c = 2 * Math.PI * r
  let offset = 0
  return (
    <div className="mb-6 grid gap-4 lg:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)]">
      <figure className="m-0 flex items-center gap-6 rounded-xl border border-line bg-raised p-5">
        <svg width="148" height="148" viewBox="0 0 140 140" role="img" aria-label={`${approved} approved, ${denied} denied, ${pending} waiting`}>
          <circle cx="70" cy="70" r={r} fill="none" stroke="var(--line)" strokeWidth="16" />
          {total > 0 &&
            segments.map((sg) => {
              const len = (sg.n / total) * c
              const el = (
                <circle
                  key={sg.label}
                  cx="70"
                  cy="70"
                  r={r}
                  fill="none"
                  stroke={sg.color}
                  strokeWidth="16"
                  strokeDasharray={`${len} ${c - len}`}
                  strokeDashoffset={-offset}
                  transform="rotate(-90 70 70)"
                  style={{ transition: 'stroke-dasharray .6s ease' }}
                />
              )
              offset += len
              return el
            })}
          <text x="70" y="66" textAnchor="middle" fontSize="28" fontWeight="800" fill="var(--ink)">
            {total}
          </text>
          <text x="70" y="86" textAnchor="middle" fontSize="11" fill="var(--muted)">
            requests
          </text>
        </svg>
        <figcaption className="flex-1">
          <div className="text-[11px] font-bold uppercase tracking-[0.1em] text-faint">How requests ended</div>
          <ul className="m-0 mt-2 list-none space-y-2 p-0">
            {segments.map((sg) => (
              <li key={sg.label} className="flex items-center gap-3">
                <span className="h-3 w-3 rounded-full" style={{ background: sg.color }} aria-hidden="true" />
                <span className="flex-1">{sg.label}</span>
                <span className="font-bold tabular-nums">{sg.n}</span>
                <span className="meta w-12 text-right tabular-nums">{total ? Math.round((sg.n / total) * 100) : 0}%</span>
              </li>
            ))}
          </ul>
        </figcaption>
      </figure>
      <div className="grid grid-cols-3 gap-3">
        {[
          { label: 'Decided as received', value: realTime, note: 'no person needed', tone: 'text-accent' },
          { label: 'Median turnaround', value: median == null ? '—' : `${median.toFixed(1)} h`, note: 'standard requests', tone: 'text-info' },
          { label: 'Late', value: late, note: late ? 'past the deadline' : 'none past the deadline', tone: late ? 'text-bad' : 'text-ok' },
        ].map((k) => (
          <div key={k.label} className="flex flex-col justify-between rounded-xl border border-line bg-raised p-4">
            <span className="text-[11px] font-bold uppercase tracking-[0.1em] text-faint">{k.label}</span>
            <span className={`mt-2 text-3xl font-extrabold tabular-nums ${k.tone}`}>{k.value}</span>
            <span className="meta text-xs">{k.note}</span>
          </div>
        ))}
      </div>
    </div>
  )
}
