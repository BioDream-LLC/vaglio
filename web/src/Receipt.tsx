import qrcode from 'qrcode-generator'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { errorText, when } from './common'
import { IconCheck, IconCross, IconLock, Mark } from './Icons'
import { useReceiptQuery } from './store'
import type { CaseDetail, ReceiptCheck } from './types'

/** Where a receipt is checked: this server's public receipt page. */
export function receiptURL(code: string, origin = window.location.origin): string {
  return `${origin}/#/receipt/${encodeURIComponent(code)}`
}

/** The receipt code a hash like #/receipt/VG-… names, or null. */
export function receiptFromHash(hash: string): string | null {
  const m = /^#\/receipt(?:\/([^/?#]*))?/.exec(hash)
  if (!m) return null
  return m[1] ? decodeURIComponent(m[1]) : ''
}

/** A QR code drawn as SVG squares, so it is crisp at any size and needs no image. */
export function QR({ text, size = 132, label }: { text: string; size?: number; label: string }) {
  const cells = useMemo(() => {
    const q = qrcode(0, 'M')
    q.addData(text)
    q.make()
    const n = q.getModuleCount()
    const dark: [number, number][] = []
    for (let r = 0; r < n; r++) for (let c = 0; c < n; c++) if (q.isDark(r, c)) dark.push([c, r])
    return { n, dark }
  }, [text])
  const m = 2 // quiet zone, in cells
  return (
    <svg
      width={size}
      height={size}
      viewBox={`0 0 ${cells.n + 2 * m} ${cells.n + 2 * m}`}
      role="img"
      aria-label={label}
      shapeRendering="crispEdges"
      className="rounded-lg"
    >
      <rect width="100%" height="100%" fill="#fff" />
      <path fill="#0b1020" d={cells.dark.map(([x, y]) => `M${x + m} ${y + m}h1v1h-1z`).join('')} />
    </svg>
  )
}

const actionText: Record<string, string> = {
  'case.opened': 'The engine’s decision',
  'case.reviewed': 'The reviewer’s decision',
  'case.appealed': 'The appeal’s outcome',
}

/** The receipts of a case's decisions, each with its QR code, as the case screen shows them. */
export function CaseReceipts({ c }: { c: CaseDetail }) {
  if (!c.receipts || c.receipts.length === 0) return null
  return (
    <>
      <h3>Receipts</h3>
      <p className="meta">
        Each decision has a receipt anyone can check against this server&apos;s tamper-evident log, without signing in
        and without seeing anything about the patient. Put it in the letter.
      </p>
      <ul className="m-0 grid list-none gap-3 p-0 md:grid-cols-2">
        {c.receipts.map((r) => (
          <li key={r.receipt} className="receipt-card">
            <QR text={receiptURL(r.receipt)} size={104} label={`QR code for receipt ${r.receipt}`} />
            <div className="min-w-0">
              <div className="text-[11px] font-bold uppercase tracking-[0.1em] text-faint">
                {actionText[r.action] ?? r.action}
              </div>
              <div className="receipt-code">{r.receipt}</div>
              <div className="meta text-xs">
                {when(r.time)} · record {r.seq}
              </div>
              <a className="text-sm" href={receiptURL(r.receipt)} target="_blank" rel="noreferrer">
                Check it
              </a>
            </div>
          </li>
        ))}
      </ul>
    </>
  )
}

const decisionWords: Record<ReceiptCheck['decision'], string> = {
  approve: 'Approved',
  deny: 'Denied',
  pend: 'Pended: more information or a clinician needed',
  overturned: 'Approved on appeal',
  upheld: 'Denial upheld on appeal',
}

const byWords: Record<ReceiptCheck['by'], string> = {
  engine: 'the plan’s policy, applied by Vaglio',
  reviewer: 'a reviewer',
  clinician: 'a licensed clinician',
  appeal: 'the appeal',
}

/**
 * The public receipt page: anyone with a receipt (a patient, a provider, a regulator) checks it here, signed in or not.
 * It shows the decision, the policy, and where the record sits in the log; nothing about the patient.
 */
export function ReceiptPage({ code }: { code: string }) {
  const [typed, setTyped] = useState(code)
  useEffect(() => setTyped(code), [code])
  const { data, error, isFetching } = useReceiptQuery(code, { skip: !code })
  function submit(e: FormEvent) {
    e.preventDefault()
    window.location.hash = `#/receipt/${encodeURIComponent(typed.trim())}`
  }
  return (
    <div className="layout">
      <main className="mx-auto w-full max-w-2xl py-10">
        <div className="mb-6 flex items-center gap-3">
          <Mark size={44} />
          <div>
            <div className="text-xl font-extrabold">Check a decision</div>
            <div className="meta text-sm">A prior authorization decision made with Vaglio</div>
          </div>
        </div>
        <section aria-labelledby="receipt-h">
          <h2 id="receipt-h" className="sr-only">
            Receipt
          </h2>
          <form onSubmit={submit} className="flex flex-wrap items-end gap-2">
            <div className="min-w-[16rem] flex-1">
              <label htmlFor="receipt-code">Receipt</label>
              <input
                id="receipt-code"
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                placeholder="VG-1A2B-3C4D-5E6F-7A8B"
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
              />
            </div>
            <button type="submit">Check</button>
          </form>
          <div aria-live="polite">
            {isFetching && <p className="meta">Checking…</p>}
            {error && !isFetching && (
              <p role="alert" className="note warn mt-4">
                {errorText(error, 'The receipt could not be checked.')}
              </p>
            )}
            {data && !isFetching && <ReceiptResult r={data} />}
          </div>
        </section>
        <p className="meta mt-6 text-xs">
          A receipt names one record in this server&apos;s audit log, a chain in which every record carries the hash of the
          one before: a record changed after the fact no longer matches its receipt, and breaks the chain after it. This
          page shows nothing about the patient.
        </p>
      </main>
    </div>
  )
}

function ReceiptResult({ r }: { r: ReceiptCheck }) {
  const good = r.decision === 'approve' || r.decision === 'overturned'
  // The panel says the receipt is genuine, so it is green whatever the decision; the decision has its own colour.
  return (
    <article className="result approved mt-5">
      <h3>
        <span className="tone-icon" aria-hidden="true">
          <IconLock size={20} />
        </span>
        Genuine receipt
      </h3>
      <dl className="facts">
        <dt>Receipt</dt>
        <dd className="font-mono">{r.receipt}</dd>
        <dt>Decision</dt>
        <dd className="flex items-center gap-2 font-semibold">
          {good ? <IconCheck className="text-ok" /> : r.decision === 'pend' ? null : <IconCross className="text-bad" />}
          {decisionWords[r.decision]}
          {r.goldCard && ' (the provider is exempt from review)'}
        </dd>
        <dt>Made by</dt>
        <dd>
          {byWords[r.by]}
          {r.credential && `, ${r.credential}`}
          {r.proposed && '. The engine proposed a denial and left it to a clinician.'}
        </dd>
        <dt>When</dt>
        <dd>{when(r.time)}</dd>
        <dt>Policy</dt>
        <dd>
          {r.policyTitle} <span className="meta">v{r.policyVersion}</span>
        </dd>
        {r.clause && (
          <>
            <dt>Rests on</dt>
            <dd>“{r.clause}”</dd>
          </>
        )}
        <dt>In the log</dt>
        <dd>
          Record {r.record.seq} of {r.chain.records}; the chain verifies from its first record.
          <div className="meta break-all font-mono text-xs">record {r.record.hash}</div>
          <div className="meta break-all font-mono text-xs">head {r.chain.head}</div>
        </dd>
      </dl>
      {r.later && r.later.length > 0 && (
        <div className="note mt-3">
          <strong>A later decision replaced this one.</strong>
          <ul className="mt-1">
            {r.later.map((l) => (
              <li key={l.receipt}>
                {decisionWords[l.decision]} by {byWords[l.by]}, {when(l.time)}:{' '}
                <a href={`#/receipt/${encodeURIComponent(l.receipt)}`} className="font-mono">
                  {l.receipt}
                </a>
              </li>
            ))}
          </ul>
        </div>
      )}
    </article>
  )
}
