import { useState } from 'react'
import { errorText, when } from './common'
import { useAssignMutation, useEscalateMutation, usePeerToPeerMutation, useReceiveAppealMutation } from './store'
import { can, type AppealRequest, type CaseDetail, type Me } from './types'

const fromText: Record<AppealRequest['from'], string> = {
  provider: 'The provider',
  member: 'The member',
  representative: "The member's representative",
}

/** The work on an open case: who has it, sending it to a physician, peer-to-peer calls. */
export function WorkPanel({ c, me }: { c: CaseDetail; me: Me }) {
  const open = c.status === 'pended' || (!!c.appealTaken && !c.appeal)
  const working = me.role !== 'viewer'
  const [assign, assigned] = useAssignMutation()
  const holder = c.assigned?.to ?? ''
  if (!open) return null
  return (
    <section className="note" aria-label="Work on this case">
      <p className="m-0">
        {holder ? (
          <>
            <strong>{holder === me.user ? 'You have this case' : `${holder} has this case`}</strong>{' '}
            <span className="meta">(since {when(c.assigned!.time)})</span>
          </>
        ) : (
          <strong>Nobody has this case yet.</strong>
        )}{' '}
        {working && !holder && (
          <button type="button" className="link" disabled={assigned.isLoading} onClick={() => assign({ id: c.id, to: me.user })}>
            Take it
          </button>
        )}
        {working && holder && (holder === me.user || me.role === 'admin') && (
          <button type="button" className="link" disabled={assigned.isLoading} onClick={() => assign({ id: c.id, to: '' })}>
            Hand it back to the queue
          </button>
        )}
      </p>
      {me.role === 'admin' && <GiveTo c={c} />}
      {assigned.error && <p role="alert">{errorText(assigned.error, 'Not changed.')}</p>}
      {c.escalated ? (
        <p className="m-0 text-violet-400">
          <strong>Sent to a physician</strong> by {c.escalated.by}, {when(c.escalated.time)}: {c.escalated.note}. Only a clinician
          decides it.
        </p>
      ) : (
        working && c.status === 'pended' && <Escalate id={c.id} />
      )}
      {c.appealTaken && !c.appeal && (
        <p className="m-0 text-sky-400">
          <strong>Appeal received</strong> {when(c.appealTaken.received)} from {fromText[c.appealTaken.from].toLowerCase()}
          {c.appealTaken.expedited ? ', expedited' : ''}: {c.appealTaken.reason}. Due {when(c.appealTaken.due)}. Decided by someone
          other than {c.appealTaken.denier === 'engine' ? 'the engine' : c.appealTaken.denier}; a denial is upheld only by a
          clinician.
        </p>
      )}
      <PeerToPeerCalls c={c} me={me} />
    </section>
  )
}

function GiveTo({ c }: { c: CaseDetail }) {
  const [to, setTo] = useState('')
  const [assign, { error, isLoading }] = useAssignMutation()
  return (
    <form
      className="flex flex-wrap items-end gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        if (to.trim()) assign({ id: c.id, to: to.trim() })
      }}
    >
      <label htmlFor="give-to">Give it to (account name)</label>
      <input id="give-to" value={to} onChange={(e) => setTo(e.target.value)} />
      <button type="submit" disabled={isLoading || !to.trim()}>
        Give
      </button>
      {error && <p role="alert">{errorText(error, 'Not given.')}</p>}
    </form>
  )
}

function Escalate({ id }: { id: string }) {
  const [note, setNote] = useState('')
  const [escalate, { error, isLoading }] = useEscalateMutation()
  return (
    <details className="review">
      <summary>Send to a physician</summary>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          escalate({ id, note: note.trim() })
        }}
      >
        <label htmlFor="escalate-note">What the physician is asked to look at</label>
        <textarea id="escalate-note" required value={note} onChange={(e) => setNote(e.target.value)} />
        <button type="submit" disabled={isLoading || !note.trim()}>
          Send
        </button>
        {error && <p role="alert">{errorText(error, 'Not sent.')}</p>}
      </form>
    </details>
  )
}

const callText = { scheduled: 'Set for', held: 'Held', missed: 'Missed', cancelled: 'Cancelled' } as const

function PeerToPeerCalls({ c, me }: { c: CaseDetail; me: Me }) {
  const [at, setAt] = useState('')
  const [withWhom, setWith] = useState('')
  const [note, setNote] = useState('')
  const [call, { error, isLoading }] = usePeerToPeerMutation()
  const working = me.role !== 'viewer'
  const calls = c.peerToPeer ?? []
  return (
    <>
      {calls.length > 0 && (
        <ul className="m-0">
          {calls.map((p) => (
            <li key={p.id}>
              Peer-to-peer with {p.with}: {callText[p.status]} {when(p.at)}
              {p.note && <> — {p.note}</>}{' '}
              <span className="meta">(by {p.by})</span>
              {working && p.status === 'scheduled' && (
                <>
                  {' '}
                  <button
                    type="button"
                    className="link"
                    disabled={isLoading || !note.trim()}
                    title="Write what was said below first"
                    onClick={() => call({ id: c.id, call: p.id, status: 'held', note: note.trim() })}
                  >
                    Held
                  </button>{' '}
                  <button type="button" className="link" disabled={isLoading} onClick={() => call({ id: c.id, call: p.id, status: 'missed' })}>
                    Missed
                  </button>{' '}
                  <button
                    type="button"
                    className="link"
                    disabled={isLoading}
                    onClick={() => call({ id: c.id, call: p.id, status: 'cancelled' })}
                  >
                    Cancel
                  </button>
                </>
              )}
            </li>
          ))}
        </ul>
      )}
      {working && calls.some((p) => p.status === 'scheduled') && (
        <>
          <label htmlFor="p2p-note">What was said on the call</label>
          <textarea id="p2p-note" value={note} onChange={(e) => setNote(e.target.value)} />
        </>
      )}
      {working && (
        <details className="review">
          <summary>Offer the ordering provider a peer-to-peer call</summary>
          <form
            onSubmit={(e) => {
              e.preventDefault()
              call({ id: c.id, at: new Date(at).toISOString().replace(/\.\d+Z$/, 'Z'), with: withWhom.trim() })
            }}
          >
            <label htmlFor="p2p-at">When</label>
            <input id="p2p-at" type="datetime-local" required value={at} onChange={(e) => setAt(e.target.value)} />
            <label htmlFor="p2p-with">With whom, and how to reach them</label>
            <input id="p2p-with" required value={withWhom} onChange={(e) => setWith(e.target.value)} />
            <button type="submit" disabled={isLoading || !at || !withWhom.trim()}>
              Schedule
            </button>
          </form>
        </details>
      )}
      {error && <p role="alert">{errorText(error, 'Not recorded.')}</p>}
    </>
  )
}

/** Taking in an appeal of a denial. */
export function ReceiveAppeal({ c, me }: { c: CaseDetail; me: Me }) {
  const [from, setFrom] = useState<AppealRequest['from'] | ''>('')
  const [reason, setReason] = useState('')
  const [expedited, setExpedited] = useState(false)
  const [receive, { error, isLoading }] = useReceiveAppealMutation()
  if (!can(me, 'reviewer', 'appeals') || c.appealTaken || c.appeal) return null
  return (
    <details className="review">
      <summary>An appeal arrived</summary>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (from) receive({ id: c.id, from, reason: reason.trim(), expedited })
        }}
      >
        <div className="findings" role="radiogroup" aria-label="Who appealed">
          {(Object.keys(fromText) as AppealRequest['from'][]).map((f) => (
            <label key={f}>
              <input type="radio" name="appeal-from" checked={from === f} onChange={() => setFrom(f)} required />
              {fromText[f]}
            </label>
          ))}
        </div>
        <label htmlFor="appeal-why">What it says is wrong with the denial</label>
        <textarea id="appeal-why" required value={reason} onChange={(e) => setReason(e.target.value)} />
        <label>
          <input type="checkbox" checked={expedited} onChange={(e) => setExpedited(e.target.checked)} /> Expedited (72 hours)
        </label>
        <button type="submit" disabled={isLoading || !from || !reason.trim()}>
          Take in the appeal
        </button>
        {error && <p role="alert">{errorText(error, 'Not taken in.')}</p>}
      </form>
    </details>
  )
}
