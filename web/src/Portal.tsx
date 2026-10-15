import { useState, type FormEvent } from 'react'
import { errorText, when } from './common'
import { IconDoc, IconInbox, IconLogout } from './Icons'
import { LetterView } from './LetterView'
import {
  useLogoutMutation,
  usePortalAppealMutation,
  usePortalDocumentsMutation,
  usePortalFormQuery,
  usePortalMeQuery,
  usePortalRequestsQuery,
  usePortalSubmitMutation,
} from './store'
import type { IntakeBody, Me, PortalDocument, PortalRequest } from './types'

const statusText: Record<PortalRequest['status'], string> = {
  approved: 'Approved',
  denied: 'Not approved',
  waiting: 'Waiting for information',
  'with-a-clinician': 'With a clinician',
  replaced: 'Replaced by a later request',
}
const statusTone: Record<PortalRequest['status'], string> = {
  approved: 'text-emerald-400',
  denied: 'text-rose-400',
  waiting: 'text-amber-400',
  'with-a-clinician': 'text-violet-400',
  replaced: 'text-faint',
}

function readFile(file: File): Promise<PortalDocument> {
  return new Promise((ok) => {
    const r = new FileReader()
    r.onload = () => ok({ description: file.name, contentType: file.type || 'application/pdf', data: String(r.result).replace(/^data:[^,]*,/, '') })
    r.readAsDataURL(file)
  })
}

/** The provider portal: a provider's office sends requests and documents, sees decisions and their reasons, appeals. */
export function Portal({ me }: { me: Me }) {
  const { data: who } = usePortalMeQuery()
  const { data, error } = usePortalRequestsQuery(undefined, { pollingInterval: 20000, refetchOnMountOrArgChange: true })
  const [logout] = useLogoutMutation()
  const [open, setOpen] = useState<string | null>(null)
  const [sending, setSending] = useState(false)
  const shown = data?.find((r) => r.id === open)
  return (
    <div className="mx-auto max-w-[60rem] px-6 py-6">
      <header className="mb-6 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="m-0 text-xl font-extrabold">Prior authorization: provider portal</h1>
          <p className="meta m-0">
            Signed in as {me.user}, for NPI {who?.npis.join(', ') ?? '…'}. You see the requests of these NPIs only.
          </p>
        </div>
        <button type="button" className="link flex items-center gap-1" onClick={() => logout()}>
          <IconLogout size={16} /> Sign out
        </button>
      </header>
      {!sending && (
        <button type="button" onClick={() => setSending(true)}>
          Send a new request
        </button>
      )}
      {sending && who && <NewRequest npis={who.npis} onDone={(id) => (setSending(false), setOpen(id))} onCancel={() => setSending(false)} />}
      {shown && <Detail r={shown} onClose={() => setOpen(null)} />}
      <h2 className="mt-6 flex items-center gap-2">
        <IconInbox className="text-sky-400" /> Your requests
      </h2>
      {error && <p role="alert">{errorText(error, 'Could not load your requests.')}</p>}
      {data && data.length === 0 && <p>No requests yet.</p>}
      {data && data.length > 0 && (
        <table className="cases">
          <thead>
            <tr>
              <th scope="col">Sent</th>
              <th scope="col">Service</th>
              <th scope="col">Patient</th>
              <th scope="col">Status</th>
            </tr>
          </thead>
          <tbody>
            {data.map((r) => (
              <tr key={r.id}>
                <td>
                  <a
                    href={`#/portal/${r.id}`}
                    onClick={(e) => {
                      e.preventDefault()
                      setOpen(r.id)
                    }}
                  >
                    {when(r.received)}
                  </a>
                </td>
                <td>{r.title}</td>
                <td>{r.patient}</td>
                <td className={statusTone[r.status]}>
                  {statusText[r.status]}
                  {r.appeal && <span className="meta"> · appeal {r.appeal}</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

function Detail({ r, onClose }: { r: PortalRequest; onClose: () => void }) {
  const [files, setFiles] = useState<File[]>([])
  const [send, sent] = usePortalDocumentsMutation()
  const [reason, setReason] = useState('')
  const [expedited, setExpedited] = useState(false)
  const [appeal, appealed] = usePortalAppealMutation()
  return (
    <article className="note mt-4" aria-label="The request">
      <p className="m-0 flex justify-between gap-3">
        <strong className={statusTone[r.status]}>
          {r.title}: {statusText[r.status]}
        </strong>
        <button type="button" className="link" onClick={onClose}>
          Close
        </button>
      </p>
      {r.why && <p>{r.why}</p>}
      {r.due && <p className="meta">A decision is due by {when(r.due)}.</p>}
      {r.requested && r.requested.length > 0 && (
        <>
          <p className="m-0">What is needed:</p>
          <ul>
            {r.requested.map((q) => (
              <li key={q}>{q}</li>
            ))}
          </ul>
        </>
      )}
      {r.receipt && <p className="meta">Receipt {r.receipt}: anyone can check this decision with it.</p>}
      {(r.status === 'waiting' || r.status === 'with-a-clinician' || r.appeal === 'received') && (
        <form
          onSubmit={async (e: FormEvent) => {
            e.preventDefault()
            const documents = await Promise.all(files.map(readFile))
            const res = await send({ id: r.id, documents })
            if (!('error' in res)) setFiles([])
          }}
        >
          <label>
            Send documents for this request <span className="meta">(PDF, TIFF, PNG, JPEG or text, up to 8 MB each)</span>
            <input
              type="file"
              multiple
              accept="application/pdf,image/tiff,image/png,image/jpeg,text/plain"
              onChange={(e) => setFiles([...(e.target.files ?? [])])}
            />
          </label>
          <button type="submit" disabled={sent.isLoading || files.length === 0}>
            <IconDoc size={16} className="inline" /> Send
          </button>
          {sent.isSuccess && <p role="status">Sent. The reviewer sees them with the request.</p>}
          {sent.error && <p role="alert">{errorText(sent.error, 'Not sent.')}</p>}
        </form>
      )}
      {r.status === 'denied' && !r.appeal && (
        <details className="review">
          <summary>Appeal this decision</summary>
          <form
            onSubmit={(e) => {
              e.preventDefault()
              appeal({ id: r.id, reason: reason.trim(), expedited })
            }}
          >
            <label htmlFor="portal-appeal">Why the decision should change</label>
            <textarea id="portal-appeal" required value={reason} onChange={(e) => setReason(e.target.value)} />
            <label>
              <input type="checkbox" checked={expedited} onChange={(e) => setExpedited(e.target.checked)} /> Expedited: waiting could
              seriously harm the patient's life, health or ability to function
            </label>
            <button type="submit" disabled={appealed.isLoading || !reason.trim()}>
              Send the appeal
            </button>
            {appealed.error && <p role="alert">{errorText(appealed.error, 'Not sent.')}</p>}
          </form>
        </details>
      )}
      {r.appeal === 'received' && r.appealDue && <p className="meta">Your appeal is due to be decided by {when(r.appealDue)}.</p>}
      <LetterView id={r.id} portal />
    </article>
  )
}

const blank = (npi: string): Omit<IntakeBody, 'channel'> => ({
  priority: '',
  patient: { family: '', given: '', birthDate: '', gender: '', state: '', memberId: '' },
  coverage: { type: '' },
  provider: { name: '', npi },
  service: { system: 'cpt', code: '' },
  answers: {},
})

function NewRequest({ npis, onDone, onCancel }: { npis: string[]; onDone: (id: string) => void; onCancel: () => void }) {
  const [f, setF] = useState(blank(npis[0] ?? ''))
  const [files, setFiles] = useState<File[]>([])
  const [submit, submitting] = usePortalSubmitMutation()
  const code = f.service.code.trim()
  const { data: policies } = usePortalFormQuery({ system: f.service.system, code }, { skip: code.length < 4 })
  const questions = new Map<string, string>()
  for (const p of policies ?? []) for (const q of p.questions) if (!questions.has(q.linkId)) questions.set(q.linkId, q.text)
  const questionnaire = policies?.find((p) => p.questionnaire)?.questionnaire
  const set = (patch: Partial<typeof f>) => setF({ ...f, ...patch })
  return (
    <form
      className="grid gap-3"
      aria-labelledby="new-h"
      onSubmit={async (e) => {
        e.preventDefault()
        const documents = await Promise.all(files.map(readFile))
        const body = { ...f, questionnaire, documents: documents.length ? documents : undefined }
        if (!questionnaire) delete body.answers
        const r = await submit(body)
        if (!('error' in r)) onDone(r.data.id)
      }}
    >
      <h2 id="new-h">A new request</h2>
      <fieldset className="grid gap-2 sm:grid-cols-3">
        <legend>Patient</legend>
        <label>
          Family name
          <input required value={f.patient.family} onChange={(e) => set({ patient: { ...f.patient, family: e.target.value } })} />
        </label>
        <label>
          Given name
          <input value={f.patient.given} onChange={(e) => set({ patient: { ...f.patient, given: e.target.value } })} />
        </label>
        <label>
          Birth date
          <input required type="date" value={f.patient.birthDate} onChange={(e) => set({ patient: { ...f.patient, birthDate: e.target.value } })} />
        </label>
        <label>
          Member id
          <input required value={f.patient.memberId} onChange={(e) => set({ patient: { ...f.patient, memberId: e.target.value } })} />
        </label>
      </fieldset>
      <fieldset className="grid gap-2 sm:grid-cols-3">
        <legend>Provider and service</legend>
        <label>
          Practice
          <input required value={f.provider.name} onChange={(e) => set({ provider: { ...f.provider, name: e.target.value } })} />
        </label>
        <label>
          NPI
          <select value={f.provider.npi} onChange={(e) => set({ provider: { ...f.provider, npi: e.target.value } })}>
            {npis.map((n) => (
              <option key={n}>{n}</option>
            ))}
          </select>
        </label>
        <label>
          Service code
          <span className="flex gap-1">
            <select aria-label="Code system" value={f.service.system} onChange={(e) => set({ service: { ...f.service, system: e.target.value } })}>
              <option value="cpt">CPT</option>
              <option value="hcpcs">HCPCS</option>
              <option value="rxnorm">RxNorm</option>
            </select>
            <input aria-label="Code" required value={f.service.code} onChange={(e) => set({ service: { ...f.service, code: e.target.value.trim() } })} />
          </span>
        </label>
        <label>
          Priority
          <select value={f.priority} onChange={(e) => set({ priority: e.target.value as '' | 'stat' })}>
            <option value="">Standard</option>
            <option value="stat">Expedited</option>
          </select>
        </label>
      </fieldset>
      {code.length >= 4 && policies && policies.length === 0 && <p className="meta">This service does not need prior authorization here.</p>}
      {questions.size > 0 && (
        <fieldset>
          <legend>What the patient's record shows</legend>
          {[...questions].map(([id, text]) => (
            <div key={id} className="flex flex-wrap items-center justify-between gap-2 border-b border-line py-1.5" role="radiogroup" aria-label={text}>
              <span>{text}</span>
              <span className="seg">
                {(['yes', 'no', ''] as const).map((v) => (
                  <label key={v || 'none'} className={(f.answers?.[id] ?? '') === v ? 'on' : undefined}>
                    <input type="radio" name={id} checked={(f.answers?.[id] ?? '') === v} onChange={() => set({ answers: { ...f.answers, [id]: v } })} />
                    {v === 'yes' ? 'Yes' : v === 'no' ? 'No' : 'Not known'}
                  </label>
                ))}
              </span>
            </div>
          ))}
          <p className="meta">An answer left "not known" is never held against the patient: the request waits for it.</p>
        </fieldset>
      )}
      <label>
        Documents <span className="meta">(PDF, TIFF, PNG, JPEG or text, up to 8 MB each)</span>
        <input type="file" multiple accept="application/pdf,image/tiff,image/png,image/jpeg,text/plain" onChange={(e) => setFiles([...(e.target.files ?? [])])} />
      </label>
      <p className="flex gap-2">
        <button type="submit" disabled={submitting.isLoading || !policies?.length}>
          {submitting.isLoading ? 'Sending…' : 'Send the request'}
        </button>
        <button type="button" className="link" onClick={onCancel}>
          Cancel
        </button>
      </p>
      {submitting.error && <p role="alert">{errorText(submitting.error, 'Not sent.')}</p>}
    </form>
  )
}
