import { useEffect, useMemo, useRef, useState } from 'react'
import { Result, errorText, outcomeText } from './common'
import { IconRefresh, IconSparkle } from './Icons'
import { useDecideMutation, usePoliciesQuery } from './store'
import type { Assertion, CaseDetail, DecideRequest, DecideResult, Finding } from './types'

/** What the sandbox holds for one piece of evidence. */
export type Supposed = Finding | 'unaddressed'

/** The quote a supposed finding carries: the engine wants provenance, and this says plainly there is none. */
export const SUPPOSED = { reference: 'what-if', quote: 'Supposed in the what-if sandbox; not in the record.' }

/** The findings a case was decided on, one per piece of evidence, as the engine read them (contradicted evidence is
 * left unaddressed, as the engine treats it). */
export function baseline(c: Pick<CaseDetail, 'evidence'>): Record<string, Assertion> {
  const contradicted = new Set((c.evidence.contradictions ?? []).map((x) => x.argument))
  const out: Record<string, Assertion> = {}
  for (const f of c.evidence.findings ?? []) {
    if (contradicted.has(f.argument) || out[f.argument]) continue
    out[f.argument] = { argument: f.argument, finding: f.finding, reference: f.reference, quote: f.quote }
  }
  return out
}

/** The request the sandbox sends: the record's own citation where the finding is unchanged, the supposed one where not. */
export function sandboxRequest(
  policy: string,
  base: Record<string, Assertion>,
  state: Record<string, Supposed>,
): DecideRequest {
  const evidence: Assertion[] = []
  for (const [argument, f] of Object.entries(state)) {
    if (f === 'unaddressed') continue
    const b = base[argument]
    evidence.push(b && b.finding === f ? b : { argument, finding: f, ...SUPPOSED })
  }
  return { policy, evidence }
}

/** Every piece of evidence whose supposed finding differs from the record's. */
export function changed(base: Record<string, Assertion>, state: Record<string, Supposed>): Set<string> {
  const out = new Set<string>()
  for (const [argument, f] of Object.entries(state)) {
    if ((base[argument]?.finding ?? 'unaddressed') !== f) out.add(argument)
  }
  return out
}

const choices: { value: Supposed; label: string }[] = [
  { value: 'present', label: 'Present' },
  { value: 'absent', label: 'Absent' },
  { value: 'unaddressed', label: 'Not addressed' },
]

/**
 * The what-if sandbox: flip any piece of evidence and see the decision and its proof redrawn, with what you changed lit
 * up in the proof. Nothing here is recorded or sent anywhere but the engine; the case keeps the decision it was given.
 */
export function WhatIf({ c, original }: { c: CaseDetail; original: DecideResult }) {
  const { data: policies } = usePoliciesQuery()
  const policy = policies?.find((p) => p.id === c.policy)
  const base = useMemo(() => baseline(c), [c])
  const initial = useMemo(() => {
    const s: Record<string, Supposed> = {}
    for (const a of policy?.evidence ?? []) s[a.name] = base[a.name]?.finding ?? 'unaddressed'
    return s
  }, [policy, base])
  const [state, setState] = useState<Record<string, Supposed>>(initial)
  useEffect(() => setState(initial), [initial])
  const [decide, { data, error, isLoading }] = useDecideMutation()
  const diff = useMemo(() => changed(base, state), [base, state])
  const timer = useRef<number | undefined>(undefined)

  useEffect(() => {
    if (!policy || diff.size === 0) return
    window.clearTimeout(timer.current)
    // The case's own version: an older one comes from the versions the server kept.
    const req = { ...sandboxRequest(c.policy, base, state), version: c.policyVersion }
    timer.current = window.setTimeout(() => decide(req), 150)
    return () => window.clearTimeout(timer.current)
  }, [policy, state, diff, base, c.policy, c.policyVersion, decide])

  if (!policy) return null
  if (error && policy.version !== c.policyVersion && diff.size > 0)
    return (
      <p className="meta">
        The what-if sandbox needs the policy this case was decided on (v{c.policyVersion}), and this server did not keep
        it; v{policy.version} is in force now.
      </p>
    )
  const shown = diff.size === 0 ? original : data
  const flipped = shown && shown.outcome !== original.outcome
  return (
    <div className="whatif">
      <p className="meta">
        Change what the record shows and watch the decision redraw. Nothing here is recorded, and the case keeps its
        decision.
      </p>
      <ul className="whatif-list">
        {policy.evidence.map((a) => (
          <li key={a.name} className={diff.has(a.name) ? 'is-changed' : undefined}>
            <span className="whatif-text">{a.text}</span>
            <span className="seg" role="radiogroup" aria-label={`What if: ${a.text}`}>
              {choices.map((ch) => (
                <label key={ch.value} className={`seg-${ch.value}`}>
                  <input
                    type="radio"
                    name={`wi-${a.name}`}
                    value={ch.value}
                    checked={state[a.name] === ch.value}
                    onChange={() => setState((s) => ({ ...s, [a.name]: ch.value }))}
                  />
                  <span>{ch.label}</span>
                </label>
              ))}
            </span>
          </li>
        ))}
      </ul>
      <div className="flex flex-wrap items-center gap-3" aria-live="polite">
        {diff.size > 0 && (
          <button type="button" className="link flex items-center gap-1" onClick={() => setState(initial)}>
            <IconRefresh size={14} /> Back to the record
          </button>
        )}
        {diff.size > 0 && shown && (
          <span className={`whatif-verdict ${flipped ? 'flip' : 'same'}`}>
            <IconSparkle size={14} />{' '}
            {flipped
              ? `${outcomeText[original.outcome]} → ${outcomeText[shown.outcome]}`
              : `Still ${outcomeText[shown.outcome].toLowerCase()}`}
            {' · '}
            {diff.size} change{diff.size === 1 ? '' : 's'}
          </span>
        )}
        {isLoading && <span className="meta">Deciding…</span>}
      </div>
      {error && <p role="alert">{errorText(error, 'The engine could not decide this.')}</p>}
      {shown && <Result result={shown} heading={3} highlight={diff} />}
    </div>
  )
}
