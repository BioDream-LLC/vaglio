import { useState } from 'react'
import { useDispatch } from 'react-redux'
import { errorText, when } from './common'
import { IconClock, IconPhone, IconQueue, IconStethoscope } from './Icons'
import { go, useQueueQuery, useTakeNextMutation, useWorkloadQuery } from './store'
import type { Me, QueueItem } from './types'

const whoText = { all: 'Every open case', mine: 'Mine', unassigned: 'Nobody has it' } as const

/** The work queue: open cases in the order they are worked, who has each, and the next one to take. */
export function Queue({ me }: { me: Me }) {
  const [who, setWho] = useState<keyof typeof whoText>(me.role === 'viewer' ? 'all' : 'mine')
  const [physician, setPhysician] = useState(false)
  const { data, error, isFetching } = useQueueQuery(
    { who: who === 'all' ? '' : who, tier: physician ? 'physician' : '' },
    { pollingInterval: 15000, refetchOnMountOrArgChange: true },
  )
  const { data: load } = useWorkloadQuery(undefined, { pollingInterval: 30000 })
  const [takeNext, taken] = useTakeNextMutation()
  const dispatch = useDispatch()
  const working = me.role !== 'viewer'
  return (
    <section aria-labelledby="queue-h">
      <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 id="queue-h" className="m-0 flex items-center gap-2">
            <IconQueue className="text-amber-400" /> Work queue
          </h2>
          <p className="meta m-0">
            Requests waiting for a decision and appeals waiting for an outcome, in the order to work them: past the deadline
            first, then expedited, then the least time left. The order only orders; it decides nothing.
          </p>
        </div>
        {working && (
          <button
            type="button"
            disabled={taken.isLoading}
            onClick={async () => {
              const r = await takeNext()
              if ('data' in r && r.data) dispatch(go({ screen: 'case', id: r.data.id }))
            }}
          >
            Take the next case
          </button>
        )}
      </div>
      {taken.error && <p role="alert">{errorText(taken.error, 'Nothing was taken.')}</p>}
      {load && load.length > 0 && (
        <div className="mb-5 grid gap-3 sm:grid-cols-2 lg:grid-cols-4" aria-label="Open cases by reviewer">
          {load.map((l) => (
            <div key={l.reviewer || '-'} className="rounded-xl border border-line bg-raised p-4 shadow-sm">
              <span className="text-[11px] font-bold uppercase tracking-[0.1em] text-faint">{l.reviewer || 'Nobody has them'}</span>
              <div className="mt-1 text-3xl font-extrabold tabular-nums text-amber-400">{l.open}</div>
              <div className="meta text-xs">
                open
                {l.overdue > 0 && <span className="text-rose-400"> · {l.overdue} late</span>}
                {l.physician > 0 && <span className="text-violet-400"> · {l.physician} for a physician</span>}
              </div>
            </div>
          ))}
        </div>
      )}
      <div className="filters" role="group" aria-label="Show">
        {(Object.keys(whoText) as (keyof typeof whoText)[]).map((w) => (
          <button key={w} type="button" className={who === w ? 'chip on' : 'chip'} aria-pressed={who === w} onClick={() => setWho(w)}>
            {whoText[w]}
          </button>
        ))}
        <button type="button" className={physician ? 'chip on' : 'chip'} aria-pressed={physician} onClick={() => setPhysician(!physician)}>
          For a physician
        </button>
      </div>
      {error && <p role="alert">{errorText(error, 'Could not load the queue.')}</p>}
      {data && data.length === 0 && <p>Nothing here is waiting.</p>}
      {data && data.length > 0 && (
        <table className="cases" aria-busy={isFetching}>
          <thead>
            <tr>
              <th scope="col">Due</th>
              <th scope="col">Waiting for</th>
              <th scope="col">Policy</th>
              <th scope="col">Who has it</th>
              <th scope="col">Why here</th>
            </tr>
          </thead>
          <tbody>
            {data.map((q) => (
              <QueueRow key={q.id} q={q} open={() => dispatch(go({ screen: 'case', id: q.id }))} />
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}

function QueueRow({ q, open }: { q: QueueItem; open: () => void }) {
  return (
    <tr>
      <td>
        <span className={q.overdue ? 'text-rose-400 font-semibold' : ''}>
          <IconClock size={14} className="inline" /> {when(q.workDue)}
        </span>
      </td>
      <td>
        {q.appeal ? (
          <span className="text-sky-400">An appeal{q.appeal.expedited ? ', expedited' : ''}</span>
        ) : (
          <span className="text-amber-400">A decision{q.expedited ? ', expedited' : ''}</span>
        )}
        {q.physician && (
          <span className="ml-2 text-violet-400">
            <IconStethoscope size={14} className="inline" /> physician
          </span>
        )}
        {q.nextCall && (
          <span className="ml-2 text-emerald-400">
            <IconPhone size={14} className="inline" /> call {when(q.nextCall)}
          </span>
        )}
      </td>
      <td>
        <a
          href={`#/case/${encodeURIComponent(q.id)}`}
          onClick={(e) => {
            e.preventDefault()
            open()
          }}
        >
          {q.policyTitle}
        </a>
      </td>
      <td>{q.assigned || <span className="meta">nobody</span>}</td>
      <td className="meta">{q.rank.why.join(' · ')}</td>
    </tr>
  )
}
