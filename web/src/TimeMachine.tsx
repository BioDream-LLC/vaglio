import { useEffect, useState } from 'react'
import { Result, errorText, when } from './common'
import { IconClock } from './Icons'
import {
  usePolicyVersionSourceQuery,
  usePolicyVersionsQuery,
  useReplayCaseMutation,
  useReplayMutation,
} from './store'
import type { ReplayReport } from './types'

const verdictTone: Record<string, string> = { approve: 'text-ok', deny: 'text-bad', pend: 'text-due' }

/**
 * The replay time machine: slide across every version of a policy that has been in force, see which past decisions each
 * would have made differently, and open any of them to compare the two proofs side by side. Nothing changes.
 */
export function TimeMachine({ id }: { id: string }) {
  const { data: versions } = usePolicyVersionsQuery(id)
  const [at, setAt] = useState<number | null>(null)
  useEffect(() => {
    if (versions && versions.length > 0 && at === null) setAt(Math.max(0, versions.length - 2))
  }, [versions, at])
  const v = versions && at !== null ? versions[at] : undefined
  const { data: source } = usePolicyVersionSourceQuery({ id, sha: v?.sha256 ?? '' }, { skip: !v })
  const [replay, { data: rep, error, isLoading }] = useReplayMutation()
  useEffect(() => {
    if (source) replay(source)
  }, [source, replay])

  if (!versions || versions.length === 0) return null
  return (
    <div className="mt-6">
      <h3 className="flex items-center gap-2">
        <IconClock size={16} /> Time machine
      </h3>
      <p className="meta">
        Every version of this policy that has been in force. Choose one to see how it would have decided every past
        request, and what it would have decided differently. Nothing changes.
      </p>
      {versions.length === 1 ? (
        <p className="note">
          Only one version has been in force so far (v{versions[0].version}, since {when(versions[0].since)}). When a
          new one is signed off, both are here.
        </p>
      ) : (
        <div className="tm-track">
          <input
            type="range"
            min={0}
            max={versions.length - 1}
            step={1}
            value={at ?? 0}
            onChange={(e) => setAt(Number(e.target.value))}
            aria-label="Policy version"
            aria-valuetext={v ? `version ${v.version}, in force from ${when(v.since)}` : undefined}
          />
          <ol className="tm-ticks" aria-hidden="true">
            {versions.map((x, i) => (
              <li key={x.sha256} className={i === at ? 'on' : undefined}>
                <button type="button" tabIndex={-1} onClick={() => setAt(i)}>
                  <span className="tm-ver">v{x.version}</span>
                  <span className="tm-since">{new Date(x.since).toLocaleDateString()}</span>
                </button>
              </li>
            ))}
          </ol>
        </div>
      )}
      <div aria-live="polite">
        {isLoading && <p className="meta">Replaying v{v?.version} over every past case…</p>}
        {error && <p role="alert">{errorText(error, 'Could not replay.')}</p>}
        {rep && !isLoading && source && <Changes rep={rep} source={source} />}
      </div>
    </div>
  )
}

function Changes({ rep, source }: { rep: ReplayReport; source: string }) {
  const [open, setOpen] = useState<string | null>(null)
  const [decide, { data: pair, error, isLoading, reset }] = useReplayCaseMutation()
  return (
    <>
      <div className="tm-summary">
        <span className="tm-big">{rep.changed}</span>
        <span>
          of {rep.cases} past decision{rep.cases === 1 ? '' : 's'} would have gone differently under v{rep.version}
          {rep.errors > 0 && `; ${rep.errors} could not be replayed`}.
        </span>
        {Object.entries(rep.transitions).map(([t, n]) => {
          const [was, now] = t.split(' → ')
          return (
            <span key={t} className="tm-chip">
              <span className={verdictTone[was]}>{was}</span> → <span className={verdictTone[now]}>{now}</span>{' '}
              <strong>{n}</strong>
            </span>
          )
        })}
      </div>
      {rep.rows.length > 0 && (
        <ul className="m-0 mt-3 list-none space-y-2 p-0">
          {rep.rows.map((r) => (
            <li key={r.case}>
              <button
                type="button"
                className={`tm-row ${open === r.case ? 'on' : ''}`}
                aria-expanded={open === r.case}
                onClick={() => {
                  if (open === r.case) {
                    setOpen(null)
                    return
                  }
                  setOpen(r.case)
                  reset()
                  if (!r.error) decide({ source, case: r.case })
                }}
              >
                <span className="meta">{when(r.created)}</span>
                <span>
                  <span className={verdictTone[r.was]}>{r.was}</span> →{' '}
                  <span className={verdictTone[r.now ?? '']}>{r.now ?? '—'}</span>
                </span>
                <span className={r.note?.startsWith('would deny') || r.error ? 'warn' : 'meta'}>{r.error ?? r.note}</span>
              </button>
              {open === r.case && (
                <div className="mt-2">
                  {isLoading && <p className="meta">Deciding it again…</p>}
                  {error && <p role="alert">{errorText(error, 'Could not decide it again.')}</p>}
                  {pair && (
                    <div className="grid gap-3 lg:grid-cols-2">
                      <div>
                        <div className="tm-side">As decided, under v{pair.was.policyVersion}</div>
                        <Result result={pair.was} heading={3} />
                      </div>
                      <div>
                        <div className="tm-side">Under v{pair.now.policyVersion}</div>
                        <Result result={pair.now} heading={3} />
                      </div>
                    </div>
                  )}
                  <p className="mt-2 text-sm">
                    <a href={`#/case/${r.case}`}>Open the case</a>
                  </p>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </>
  )
}
