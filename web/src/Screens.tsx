import { IconPolicies } from './Icons'
import { PolicyMap } from './PolicyMap'
import { TimeMachine } from './TimeMachine'
import { useState, type FormEvent } from 'react'
import { useDispatch, useSelector } from 'react-redux'
import { errorText, Result, when } from './common'
import {
  blank,
  go,
  selectPolicy,
  setEvidence,
  toRequest,
  useAuditQuery,
  useDecideMutation,
  useDraftFromMutation,
  useDraftsQuery,
  usePoliciesQuery,
  usePolicyCheckQuery,
  usePolicySourceQuery,
  useReplayMutation,
  useVerifyQuery,
  type RootState,
} from './store'
import { can, type Argument, type AuditRecord, type Me, type PolicySummary, type ReplayReport } from './types'

/** Try a policy on evidence entered by hand. Nothing is recorded. */
export function Try() {
  const { data: policies, error, isLoading } = usePoliciesQuery()
  const request = useSelector((s: RootState) => s.request)
  const dispatch = useDispatch()
  const [decide, { data: result, error: decideError, isLoading: deciding, reset }] = useDecideMutation()
  const policy = policies?.find((p) => p.id === request.policy)

  function submit(e: FormEvent) {
    e.preventDefault()
    const body = toRequest(request)
    if (body) decide(body)
  }

  return (
    <section aria-labelledby="try-h">
      <h2 id="try-h">Try a policy</h2>
      <p className="meta">Evidence entered here decides nothing and is not recorded.</p>
      {isLoading && <p>Loading policies…</p>}
      {error && <p role="alert">Could not load policies.</p>}
      {policies && (
        <form onSubmit={submit} aria-describedby="form-help">
          <label htmlFor="policy">Policy</label>
          <select
            id="policy"
            value={request.policy ?? ''}
            onChange={(e) => {
              dispatch(selectPolicy(e.target.value))
              reset()
            }}
          >
            <option value="" disabled>
              Choose a policy
            </option>
            {policies.map((p) => (
              <option key={p.id} value={p.id}>
                {p.title} (v{p.version})
              </option>
            ))}
          </select>
          {policy && (
            <>
              <p className="source">{policy.source}</p>
              <p id="form-help">
                For each item, say what the record shows. Anything left unaddressed can never be a reason to deny.
              </p>
              {policy.evidence.map((a) => (
                <EvidenceField key={a.name} arg={a} />
              ))}
              <button type="submit" disabled={deciding}>
                {deciding ? 'Deciding…' : 'Decide'}
              </button>
            </>
          )}
        </form>
      )}
      <section aria-live="polite" aria-label="Decision">
        {decideError && <p role="alert">{errorText(decideError, 'The decision request failed.')}</p>}
        {result && <Result result={result} />}
      </section>
    </section>
  )
}

function EvidenceField({ arg }: { arg: Argument }) {
  const entry = useSelector((s: RootState) => s.request.evidence[arg.name] ?? blank)
  const dispatch = useDispatch()
  const set = (patch: Partial<typeof entry>) => dispatch(setEvidence({ argument: arg.name, patch }))
  const id = `ev-${arg.name}`
  return (
    <fieldset className="evidence">
      <legend>{arg.text}</legend>
      <p className="clause">“{arg.clause}”</p>
      <div className="findings" role="radiogroup" aria-label={`What the record shows: ${arg.text}`}>
        {(['unaddressed', 'present', 'absent'] as const).map((f) => (
          <label key={f}>
            <input type="radio" name={id} checked={entry.finding === f} onChange={() => set({ finding: f })} />
            {f === 'unaddressed' ? 'Not addressed' : f === 'present' ? 'Documented present' : 'Documented absent'}
          </label>
        ))}
      </div>
      {entry.finding !== 'unaddressed' && (
        <div className="citation">
          <label htmlFor={`${id}-ref`}>Record reference</label>
          <input
            id={`${id}-ref`}
            required
            placeholder="DocumentReference/123"
            value={entry.reference}
            onChange={(e) => set({ reference: e.target.value })}
          />
          <label htmlFor={`${id}-quote`}>Quote from the record</label>
          <textarea id={`${id}-quote`} required value={entry.quote} onChange={(e) => set({ quote: e.target.value })} />
        </div>
      )}
    </fieldset>
  )
}

export function Policies() {
  const { data, error } = usePoliciesQuery()
  const dispatch = useDispatch()
  return (
    <section aria-labelledby="pol-h">
      <h2 id="pol-h">Policies</h2>
      {error && <p role="alert">Could not load policies.</p>}
      <p className="meta -mt-2 mb-4">The plan&apos;s policies in force. Each is checked on every combination of evidence before it can decide.</p>
      <ul className="m-0 grid list-none gap-3 p-0 md:grid-cols-2 xl:grid-cols-3">
        {data?.map((p) => (
          <li key={p.id}>
            <a
              href={`#/policy/${p.id}`}
              aria-disabled={!p.version || undefined}
              className="group flex h-full flex-col rounded-xl border border-line bg-raised p-4 no-underline transition hover:-translate-y-0.5 hover:border-accent hover:no-underline hover:shadow-lg"
              onClick={(e) => {
                e.preventDefault()
                if (p.version) dispatch(go({ screen: 'policy', id: p.id }))
              }}
            >
              <span className="flex items-start gap-3">
                <span className="grid h-9 w-9 flex-none place-items-center rounded-lg bg-gradient-to-br from-violet-500 to-fuchsia-600 text-white shadow">
                  <IconPolicies size={18} />
                </span>
                <span className="font-semibold text-ink group-hover:text-accent">{p.title}</span>
              </span>
              <span className="mt-3 flex flex-wrap gap-1.5">
                {p.services.map((sv) => (
                  <span key={sv.code} className="rounded-md bg-surface px-2 py-0.5 font-mono text-xs text-muted ring-1 ring-line">
                    {sv.code}
                  </span>
                ))}
              </span>
              <PolicyScope p={p} />
              <span className="meta mt-auto pt-3 text-xs">
                {p.version ? `v${p.version}` : 'Not in force yet'} · {p.evidence.length} kinds of evidence
              </span>
            </a>
          </li>
        ))}
      </ul>
    </section>
  )
}

const authorityName: Record<string, string> = {
  ncd: 'National coverage determination',
  lcd: 'Local coverage determination',
  state: 'State criteria',
  plan: 'Plan policy',
}

/** What a policy decides beyond its codes: its authority, the members and states it covers, and its days. */
export function PolicyScope({ p }: { p: PolicySummary }) {
  const tag = 'rounded-md px-2 py-0.5 text-xs ring-1'
  return (
    <span className="mt-2 flex flex-wrap gap-1.5">
      <span className={`${tag} bg-violet-500/10 text-violet-300 ring-violet-500/30`}>{authorityName[p.authority || 'plan']}</span>
      {p.lines?.map((l) => (
        <span key={l} className={`${tag} bg-sky-500/10 text-sky-300 ring-sky-500/30`}>
          {l.replace('-', ' ')}
        </span>
      ))}
      {p.states && p.states.length > 0 && (
        <span className={`${tag} bg-sky-500/10 text-sky-300 ring-sky-500/30`}>{p.states.join(' ')}</span>
      )}
      {p.effective && p.version && <span className={`${tag} text-muted ring-line`}>from {p.effective}</span>}
      {p.ends && (
        <span className={`${tag} ${p.inEffect ? 'text-muted ring-line' : 'bg-rose-500/10 text-rose-300 ring-rose-500/30'}`}>
          {p.inEffect ? `until ${p.ends}` : `ended ${p.ends}`}
        </span>
      )}
      {p.next && (
        <span className={`${tag} bg-amber-500/10 text-amber-300 ring-amber-500/30`}>
          v{p.next.version} signed, in force from {p.next.effective}
        </span>
      )}
    </span>
  )
}

export function PolicyView({ id, me }: { id: string; me: Me }) {
  const { data: source, error } = usePolicySourceQuery(id)
  const { data: check } = usePolicyCheckQuery(id)
  return (
    <section aria-labelledby="policy-h">
      <h2 id="policy-h">{id}</h2>
      {check && (
        <div className={check.ok ? 'note ok' : 'note warn'}>
          <p>
            {check.ok ? 'Passes' : 'Fails'} its check over all {check.combinations} combinations of evidence:{' '}
            {Object.entries(check.outcomes)
              .map(([k, n]) => `${k.replace('/decided', '')} ${n}`)
              .join(', ')}
            .
          </p>
          {check.warnings.length > 0 && (
            <ul>
              {check.warnings.map((w) => (
                <li key={w}>{w}</li>
              ))}
            </ul>
          )}
        </div>
      )}
      <p>
        <a href={`/api/policies/${encodeURIComponent(id)}/questionnaire`}>Download its DTR questionnaire</a>{' '}
        <span className="meta">
          made from the policy: exactly the questions its decisions read. Store it on the PAS server as
          Questionnaire/{id.replace(/_/g, '-')}.
        </span>
      </p>
      {error && <p role="alert">Could not load the policy.</p>}
      {source && me.role === 'viewer' && (
        <pre className="source-code" tabIndex={0} aria-label="Policy source">
          {source}
        </pre>
      )}
      <PolicyMap id={id} />
      {me.editing && can(me, 'admin', 'author') && <EditAsDraft id={id} />}
      {me.role !== 'viewer' && <TimeMachine id={id} />}
      {source && me.role !== 'viewer' && <Replay source={source} />}
    </section>
  )
}

/** Starts the next version of a policy in force as a draft, or opens the one already started. */
function EditAsDraft({ id }: { id: string }) {
  const { data: drafts } = useDraftsQuery()
  const [start, { error, isLoading }] = useDraftFromMutation()
  const dispatch = useDispatch()
  const open = drafts?.some((d) => d.id === id)
  return (
    <p>
      <button
        type="button"
        disabled={isLoading}
        onClick={async () => {
          if (!open) {
            const r = await start(id)
            if ('error' in r) return
          }
          dispatch(go({ screen: 'draft', id }))
        }}
      >
        {open ? 'Open its draft' : 'Edit as a new draft'}
      </button>{' '}
      <span className="meta">The version in force keeps deciding until a clinician signs the draft off.</span>
      {error && <span role="alert"> {errorText(error, 'Could not start a draft.')}</span>}
    </p>
  )
}

/** The policy's source, editable, and a replay of the edited version over the cases already decided. Nothing here
 * changes the policy in force or any case: a new version goes into the policy directory and the server is restarted. */
function Replay({ source }: { source: string }) {
  const [text, setText] = useState(source)
  const [replay, { data: rep, error, isLoading, reset }] = useReplayMutation()
  const where = error && 'data' in error ? (error.data as { line?: number; col?: number }) : undefined
  return (
    <>
      <label htmlFor="source">Source (edit it to see what a new version would change)</label>
      <textarea
        id="source"
        className="source-code"
        spellCheck={false}
        rows={Math.min(40, text.split('\n').length + 1)}
        value={text}
        onChange={(e) => {
          setText(e.target.value)
          reset()
        }}
      />
      <button type="button" disabled={isLoading} onClick={() => replay(text)}>
        {isLoading ? 'Replaying…' : 'Replay over past cases'}
      </button>{' '}
      {text !== source && (
        <button type="button" className="link" onClick={() => setText(source)}>
          Back to the version in force
        </button>
      )}
      <div aria-live="polite">
        {error && (
          <p role="alert" className="note warn">
            {errorText(error, 'Could not replay.')}
            {where?.line ? ` (line ${where.line}, column ${where.col})` : ''}
          </p>
        )}
        {rep && <ReplayResult rep={rep} />}
      </div>
    </>
  )
}

function ReplayResult({ rep }: { rep: ReplayReport }) {
  return (
    <div className={rep.changed === 0 && rep.errors === 0 ? 'note ok' : 'note'}>
      <p>
        {rep.policy} v{rep.version} {rep.checkOk ? 'passes' : <strong className="warn">fails</strong>} its check
        {rep.warnings.length > 0 && `, with ${rep.warnings.length} warning(s)`}. Over {rep.cases} past case(s):{' '}
        {rep.unchanged} unchanged, {rep.changed} changed
        {rep.errors > 0 && `, ${rep.errors} could not be replayed`}.
      </p>
      {Object.keys(rep.transitions).length > 0 && (
        <ul>
          {Object.entries(rep.transitions).map(([t, n]) => (
            <li key={t}>
              {t}: {n}
            </li>
          ))}
        </ul>
      )}
      {rep.warnings.length > 0 && (
        <ul>
          {rep.warnings.map((w) => (
            <li key={w} className="meta">
              {w}
            </li>
          ))}
        </ul>
      )}
      {rep.rows.length > 0 && (
        <table className="cases">
          <thead>
            <tr>
              <th scope="col">Case</th>
              <th scope="col">Received</th>
              <th scope="col">Was</th>
              <th scope="col">Final</th>
              <th scope="col">Now</th>
              <th scope="col">Note</th>
            </tr>
          </thead>
          <tbody>
            {rep.rows.map((r) => (
              <tr key={r.case}>
                <td>
                  <a href={`#/case/${r.case}`}>{r.case.slice(0, 8)}</a>
                </td>
                <td>{when(r.created)}</td>
                <td>{r.was}</td>
                <td>{r.final || '—'}</td>
                <td>{r.now || '—'}</td>
                <td className={r.note?.startsWith('would deny') || r.error ? 'warn' : undefined}>{r.error ?? r.note}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

export function Audit() {
  const [after, setAfter] = useState(0)
  const { data, error } = useAuditQuery(after)
  const [verifying, setVerifying] = useState(false)
  const { data: v, isFetching } = useVerifyQuery(undefined, { skip: !verifying })
  const records: AuditRecord[] = data?.records ?? []
  return (
    <section aria-labelledby="audit-h">
      <h2 id="audit-h">Audit log</h2>
      <p>
        Every decision, review, sign-in and change of access, each record chained to the one before it.{' '}
        <button type="button" onClick={() => setVerifying(true)} disabled={isFetching}>
          Verify the chain
        </button>
      </p>
      <div aria-live="polite">
        {v && v.ok && (
          <p className="note ok">
            Intact: {v.records} records. Latest hash <code>{v.head}</code>. Keep it somewhere else to make the log up to
            it unrewritable unnoticed.
          </p>
        )}
        {v && !v.ok && (
          <p role="alert" className="note warn">
            {v.error}
          </p>
        )}
      </div>
      {error && <p role="alert">{errorText(error, 'Could not load the audit log.')}</p>}
      <table className="cases">
        <thead>
          <tr>
            <th scope="col">#</th>
            <th scope="col">Time</th>
            <th scope="col">Who</th>
            <th scope="col">What</th>
            <th scope="col">Case</th>
          </tr>
        </thead>
        <tbody>
          {records.map((r) => (
            <tr key={r.seq}>
              <td>{r.seq}</td>
              <td>{when(r.time)}</td>
              <td>{r.actor}</td>
              <td>{r.action}</td>
              <td>{r.case ? <a href={`#/case/${r.case}`}>{r.case.slice(0, 8)}</a> : ''}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p>
        <button type="button" disabled={after === 0} onClick={() => setAfter(Math.max(0, after - 100))}>
          Earlier
        </button>{' '}
        <button type="button" disabled={!data || after + 100 >= data.total} onClick={() => setAfter(after + 100)}>
          Later
        </button>{' '}
        <span className="meta">{data && `records ${after + 1}–${after + records.length} of ${data.total}`}</span>
      </p>
    </section>
  )
}
