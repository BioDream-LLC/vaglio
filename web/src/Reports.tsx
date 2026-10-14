import { useState } from 'react'
import { errorText } from './common'
import {
  useGoldCardCountingMutation,
  useGoldCardQuery,
  useMeQuery,
  useMetricsQuery,
  useOverridesQuery,
  usePendsQuery,
  type Period,
} from './store'
import type { MetricsGroup } from './types'

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
