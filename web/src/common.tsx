import { IconAlert, IconCheck, IconClock, IconCross } from './Icons'
import type { Change, DecideResult, Item, Outcome, ProofNode, Reason } from './types'

export const outcomeText: Record<Outcome, string> = {
  approved: 'Approved',
  denied: 'Denied',
  pended: 'Pended',
  policy_defect: 'Policy defect: no decision issued',
}

export const reasonText: Record<Reason, string> = {
  decided: '',
  needs_information: 'A denial would rest on information nobody has addressed. Ask the provider for it.',
  indeterminate: 'The policy cannot settle this on the evidence. Route to clinical review.',
  unsatisfied: 'The policy rejects every decision on this evidence. It has a gap.',
  conflicted: 'The policy both approves and denies on this evidence. It contradicts itself.',
  contradictory_record: 'The record both documents and denies evidence this decision depends on. Route to clinical review.',
}

export function Result({
  result,
  heading = 2,
  highlight,
}: {
  result: DecideResult
  heading?: 2 | 3
  /** Arguments to light up in the proof: in the what-if sandbox, the evidence the reader changed. */
  highlight?: ReadonlySet<string>
}) {
  const H = heading === 2 ? 'h2' : 'h3'
  return (
    <article className={`result ${result.outcome}`}>
      <H>
        <span className="tone-icon" aria-hidden="true">
          <OutcomeIcon outcome={result.outcome} />
        </span>
        {outcomeText[result.outcome]}
      </H>
      {reasonText[result.reason] && <p>{reasonText[result.reason]}</p>}
      <Items title="Information needed" items={result.missing} />
      <Items title="Assumed absent (not addressed in the record)" items={result.assumptions} />
      {result.cautious && (
        <p className="note warn mt-2">
          Decided cautiously: {result.cautious}. Nothing is denied on a search that did not finish.
        </p>
      )}
      <Items title="Relied on as documented absent" items={result.relied} />
      <Items title="Contradicted in the record" items={result.contradicted} />
      <Routes routes={result.toApprove} />
      {result.proof && (
        <>
          <h3>{result.outcome === 'pended' ? 'The decision the engine did not issue, and its proof' : 'Proof'}</h3>
          <ul className="proof">
            <Proof node={result.proof} highlight={highlight} root />
          </ul>
        </>
      )}
      <p className="meta">
        Policy {result.policy} v{result.policyVersion}
      </p>
    </article>
  )
}

function Items({ title, items }: { title: string; items?: Item[] }) {
  if (!items || items.length === 0) return null
  return (
    <>
      <h3>{title}</h3>
      <ul>
        {items.map((i) => (
          <li key={i.argument}>
            {i.text}
            {i.citation && <Cite c={i.citation} />}
          </li>
        ))}
      </ul>
    </>
  )
}

/** Each smallest set of evidence whose documentation would approve: what to send, or what to look for. */
function Routes({ routes }: { routes?: Change[][] }) {
  if (!routes || routes.length === 0) return null
  return (
    <>
      <h3>What would approve it</h3>
      <ul>
        {routes.map((r, i) => (
          <li key={i}>
            {r.map((c, j) => (
              <span key={c.argument}>
                {j > 0 && ' and '}
                {c.text}
                {c.contradictsRecord && <span className="warn"> (the record documents it absent)</span>}
              </span>
            ))}
          </li>
        ))}
      </ul>
    </>
  )
}

function Proof({ node, highlight, root }: { node: ProofNode; highlight?: ReadonlySet<string>; root?: boolean }) {
  const lit = highlight?.has(node.argument)
  return (
    <li className={lit ? 'lit' : root && highlight ? 'deciding' : undefined}>
      <span className={`kind ${node.kind}`}>{node.kind}</span> {node.text}
      {lit && <span className="lit-tag"> changed</span>}
      <div className="clause">Policy: “{node.clause}”</div>
      {node.citation && <Cite c={node.citation} />}
      {node.defeats && node.defeats.length > 0 && (
        <ul>
          {node.defeats.map((d) => (
            <li key={d.attacker}>
              <span className="against">Against:</span> {d.text} <span className="against">— defeated by</span>
              <ul>
                <Proof node={d.by} highlight={highlight} />
              </ul>
            </li>
          ))}
        </ul>
      )}
    </li>
  )
}

export function Cite({ c }: { c: { reference: string; quote: string } }) {
  return (
    <blockquote className="cite">
      “{c.quote}” <cite>{c.reference}</cite>
    </blockquote>
  )
}

export function errorText(e: unknown, fallback = 'The request failed.'): string {
  if (e && typeof e === 'object' && 'data' in e) {
    const d = (e as { data?: { error?: string } }).data
    if (d && typeof d === 'object' && d.error) return d.error
  }
  return fallback
}

export function isUnauthorized(e: unknown): boolean {
  return !!e && typeof e === 'object' && 'status' in e && (e as { status: unknown }).status === 401
}

/** A timestamp in the reader's own zone, to the minute. */
export function when(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

export const decisionText = { approve: 'Approve', deny: 'Deny', pend: 'Pend' } as const

function OutcomeIcon({ outcome }: { outcome: Outcome }) {
  switch (outcome) {
    case 'approved':
      return <IconCheck size={20} strokeWidth={2.6} />
    case 'denied':
      return <IconCross size={20} strokeWidth={2.6} />
    case 'pended':
      return <IconClock size={20} strokeWidth={2.4} />
    default:
      return <IconAlert size={20} strokeWidth={2.2} />
  }
}
