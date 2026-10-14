import { useEffect, useState, type FormEvent } from 'react'
import { useDispatch } from 'react-redux'
import { errorText } from './common'
import { IconDoc, IconSparkle } from './Icons'
import { api, go, useDraftJobQuery, useDraftingQuery, useStartDraftingMutation } from './store'
import type { DraftJob, FindingKind } from './types'

const kindText: Record<FindingKind, { label: string; note: string; tone: string }> = {
  contradiction: {
    label: 'Approves and denies at once',
    note: 'on some records the draft both approves and denies: a hole in the document, or a misreading',
    tone: 'bad',
  },
  gap: {
    label: 'Decides nothing',
    note: 'on some records no decision holds, or a decision is never reached: a hole in the document, or a misreading',
    tone: 'due',
  },
  quote: { label: 'Not quoted word for word', note: 'the draft paraphrased the document', tone: 'info' },
  code: { label: 'A code the document does not list', note: 'the model supplied it from memory', tone: 'info' },
  syntax: { label: 'Does not parse', note: 'the draft is not in the policy language', tone: 'info' },
  lint: { label: 'Rules that change nothing', note: 'evidence or criteria with no effect', tone: 'info' },
}
const order: FindingKind[] = ['contradiction', 'gap', 'quote', 'code', 'syntax', 'lint']

function readFile(f: File): Promise<string> {
  return new Promise((ok, fail) => {
    const r = new FileReader()
    r.onload = () => ok(String(r.result).replace(/^data:[^,]*,/, ''))
    r.onerror = () => fail(r.error)
    r.readAsDataURL(f)
  })
}

/**
 * Upload a coverage document and get a draft policy, with what the checker found in every round: where the draft
 * approves and denies at once, where it decides nothing, what it failed to quote. The first two are where a person should
 * read the document again.
 */
export function FromDocument() {
  const { data: info } = useDraftingQuery()
  const [id, setId] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [start, started] = useStartDraftingMutation()
  const [job, setJob] = useState<string | null>(null)
  // A drafting still running when the screen opens is shown, whoever started it.
  useEffect(() => {
    if (info?.busy && info.last && !job) setJob(info.last)
  }, [info, job])
  const valid = /^[a-z][a-z0-9_]{1,63}$/.test(id)
  if (!info?.enabled) return null

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!file) return
    const data = await readFile(file)
    const r = await start({ id, name: file.name, contentType: file.type || 'text/plain', data })
    if ('data' in r && r.data) setJob(r.data.id)
  }

  return (
    <div className="fromdoc">
      <h3 className="flex items-center gap-2">
        <IconSparkle size={16} /> Draft from a coverage document
      </h3>
      <p className="meta">
        Upload the plan&apos;s coverage policy ({info.pdf ? 'PDF or text' : 'text'}). {info.model} drafts it in the policy
        language, and every draft is held to the checks: every clause quoted word for word, every code in the document,
        every combination of evidence decided once. You get the draft, and the list of what the checker found.{' '}
        <strong>The document is sent to the model&apos;s provider: published coverage documents only, never patient
        records.</strong>
      </p>
      {!job && (
        <form onSubmit={submit} className="flex flex-wrap items-end gap-3">
          <div>
            <label htmlFor="fd-id">Policy id</label>
            <input id="fd-id" value={id} onChange={(e) => setId(e.target.value.trim())} placeholder="knee_mri" />
          </div>
          <div>
            <label htmlFor="fd-file">Document</label>
            <input
              id="fd-file"
              type="file"
              accept={info.pdf ? '.pdf,.txt,application/pdf,text/plain' : '.txt,text/plain'}
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            />
          </div>
          <button type="submit" disabled={!valid || !file || started.isLoading}>
            {started.isLoading ? 'Sending…' : 'Draft it'}
          </button>
          {started.error && <p role="alert" className="w-full">{errorText(started.error, 'Could not start.')}</p>}
        </form>
      )}
      {job && <Progress job={job} onDone={() => setJob(null)} />}
    </div>
  )
}

function Progress({ job, onDone }: { job: string; onDone: () => void }) {
  const [poll, setPoll] = useState(2000)
  const { data } = useDraftJobQuery(job, { pollingInterval: poll })
  const dispatch = useDispatch()
  const done = !!data && data.state !== 'running'
  useEffect(() => {
    if (done) {
      setPoll(0)
      dispatch(api.util.invalidateTags(['Draft']))
    }
  }, [done, dispatch])
  if (!data) return <p className="meta">Starting…</p>
  return (
    <div aria-live="polite">
      <p className="flex items-center gap-2">
        <IconDoc size={16} /> <strong>{data.document}</strong>
        <span className={`badge ${data.state === 'passed' ? 'approve' : data.state === 'failed' ? 'deny' : 'pend'}`}>
          {data.state === 'running' ? `round ${data.rounds.length + 1}…` : data.state === 'passed' ? 'passes' : 'does not pass'}
        </span>
      </p>
      {data.state === 'passed' && data.check && (
        <p className="note ok">
          Round {data.rounds.length} passes every check, over all {data.check.combinations} combinations of evidence:{' '}
          {Object.entries(data.check.outcomes)
            .map(([k, n]) => `${k.replace('/decided', '')} ${n}`)
            .join(', ')}
          . It is a draft: a clinician reads it against the document and signs it off.
        </p>
      )}
      {data.state === 'failed' && (
        <p className="note warn">
          No draft passed in {data.rounds.length} round{data.rounds.length === 1 ? '' : 's'}
          {data.error ? ` (${data.error})` : ''}. What the checker still found in the last one is below: each is a place to
          read the document again.
        </p>
      )}
      <Findings job={data} />
      <div className="mt-3 flex gap-3">
        {data.saved && (
          <button type="button" onClick={() => dispatch(go({ screen: 'draft', id: data.policy }))}>
            Open the draft
          </button>
        )}
        {data.state !== 'running' && (
          <button type="button" className="link" onClick={onDone}>
            Draft another
          </button>
        )}
      </div>
    </div>
  )
}

function Findings({ job }: { job: DraftJob }) {
  if (job.rounds.length === 0) return null
  const last = job.rounds[job.rounds.length - 1]
  // Holes (contradictions and gaps) from every round: a later round may hide one by misreading something else. The
  // draft's own faults from the last round only, since earlier ones were fixed or replaced.
  const holes = job.rounds.flatMap((r) => r.findings).filter((f) => f.kind === 'contradiction' || f.kind === 'gap')
  const rest = (job.state === 'passed' ? job.rounds.slice(0, -1).flatMap((r) => r.findings) : last.findings).filter(
    (f) => f.kind !== 'contradiction' && f.kind !== 'gap',
  )
  const shown = [...holes, ...rest]
  if (shown.length === 0) return null
  const groups = order
    .map((k) => ({ k, items: shown.filter((f) => f.kind === k) }))
    .filter((g) => g.items.length > 0)
  return (
    <div className="mt-3">
      <h4 className="text-sm font-bold">
        {job.state === 'passed' ? 'What the checker found on the way, and the model fixed' : 'What the checker found'}
      </h4>
      <ul className="m-0 mt-2 list-none space-y-3 p-0">
        {groups.map((g) => (
          <li key={g.k} className={`finding finding-${kindText[g.k].tone}`}>
            <div className="font-semibold">
              {kindText[g.k].label} <span className="meta font-normal">· {g.items.length}</span>
            </div>
            <div className="meta text-xs">{kindText[g.k].note}</div>
            <ul className="mt-1 text-sm">
              {[...new Set(g.items.map((f) => f.text))].slice(0, 8).map((t) => (
                <li key={t}>{t}</li>
              ))}
            </ul>
          </li>
        ))}
      </ul>
    </div>
  )
}
