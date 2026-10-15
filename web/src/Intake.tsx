/**
 * Intake: requests that arrived by fax, phone, mail or a portal, keyed in by a person and decided like any other. The
 * fax inbox lists what the plan's fax service delivered; opening one puts its text beside the form.
 */
import { useState, type FormEvent } from 'react'
import { useDispatch } from 'react-redux'
import { decisionText, errorText, when } from './common'
import { IconDoc, IconInbox } from './Icons'
import {
  go,
  useFaxesQuery,
  useFaxQuery,
  useIntakeFormQuery,
  useKeyInMutation,
  useMeQuery,
  useSetAsideFaxMutation,
} from './store'
import type { Channel, IntakeBody, KeyedAnswer } from './types'

const channels: { value: Channel; label: string }[] = [
  { value: 'fax', label: 'Fax' },
  { value: 'phone', label: 'Phone' },
  { value: 'mail', label: 'Mail' },
  { value: 'portal', label: 'Portal' },
]

const coverages = [
  { value: '', label: 'Not shown' },
  { value: '111', label: 'Medicare Advantage' },
  { value: '1', label: 'Medicare' },
  { value: '2', label: 'Medicaid' },
  { value: '5', label: 'Commercial' },
]

export function Intake() {
  const [fax, setFax] = useState<string | null>(null)
  return (
    <section aria-labelledby="intake-h">
      <h2 id="intake-h">Intake</h2>
      <p className="meta -mt-2 mb-4">
        A request that arrived by fax, phone, mail or a portal, keyed in from what it shows. It is decided exactly as a
        request from a PAS server: the same policies, proof, case and audit record. An answer the request does not show is
        left not shown, which never denies: the request pends and asks for it. The decision goes to the provider by
        letter.
      </p>
      <div className="grid gap-6 lg:grid-cols-[18rem_1fr]">
        <FaxInbox open={fax} onOpen={setFax} />
        <IntakeForm fax={fax} onDone={() => setFax(null)} />
      </div>
    </section>
  )
}

function FaxInbox({ open, onOpen }: { open: string | null; onOpen: (id: string | null) => void }) {
  const { data, error } = useFaxesQuery()
  if (error) {
    const off = 'status' in error && error.status === 404
    return <p className="meta">{off ? 'No fax inbox on this server (serve -fax-token-env).' : errorText(error, 'Could not load the faxes.')}</p>
  }
  const waiting = data?.filter((f) => !f.done) ?? []
  return (
    <aside aria-labelledby="faxes-h">
      <h3 id="faxes-h" className="flex items-center gap-2">
        <IconInbox size={16} className="text-sky-400" /> Fax inbox <span className="meta">({waiting.length} waiting)</span>
      </h3>
      {data && waiting.length === 0 && <p className="meta">Nothing waiting.</p>}
      <ul className="plain">
        {waiting.map((f) => (
          <li key={f.id}>
            <button type="button" className={open === f.id ? 'chip on' : 'chip'} aria-pressed={open === f.id} onClick={() => onOpen(open === f.id ? null : f.id)}>
              {when(f.received)}
              {f.from ? ` · ${f.from}` : ''}
              {f.pages ? ` · ${f.pages} p.` : ''}
            </button>
          </li>
        ))}
      </ul>
      {open && <FaxPanel id={open} onClosed={() => onOpen(null)} />}
    </aside>
  )
}

function FaxPanel({ id, onClosed }: { id: string; onClosed: () => void }) {
  const viewing = useMeQuery().data?.role === 'viewer' // a public demonstration's viewer reads only
  const { data: f } = useFaxQuery(id)
  const [setAside, aside] = useSetAsideFaxMutation()
  const [reason, setReason] = useState('')
  if (!f) return null
  return (
    <div className="note mt-3">
      <p>
        <a href={`/api/fax/${encodeURIComponent(f.id)}/content`}>
          <IconDoc size={14} /> Download the fax
        </a>{' '}
        <span className="meta">({Math.round(f.size / 1024)} KB, kept with the case once keyed in)</span>
      </p>
      {f.text ? (
        <pre className="source-code max-h-80 overflow-auto text-xs" tabIndex={0} aria-label="The fax's text">
          {f.text}
        </pre>
      ) : (
        <p className="meta">No text was made of it: read the image.</p>
      )}
      {!viewing && <details>
        <summary>Not a request? Set it aside</summary>
        <label htmlFor="aside-reason">Why</label>
        <input id="aside-reason" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="A duplicate of an earlier fax" />
        <button
          type="button"
          disabled={!reason.trim() || aside.isLoading}
          onClick={async () => {
            const r = await setAside({ id, reason })
            if (!('error' in r)) onClosed()
          }}
        >
          Set aside
        </button>
        {aside.error && <p role="alert">{errorText(aside.error, 'Not set aside.')}</p>}
      </details>}
    </div>
  )
}

const blank = (): IntakeBody => ({
  channel: 'fax',
  priority: '',
  patient: { family: '', given: '', birthDate: '', gender: '', state: '', memberId: '' },
  coverage: { type: '' },
  provider: { name: '', npi: '' },
  service: { system: 'cpt', code: '' },
  answers: {},
})

function IntakeForm({ fax, onDone }: { fax: string | null; onDone: () => void }) {
  const viewing = useMeQuery().data?.role === 'viewer' // a public demonstration's viewer reads only
  const [f, setF] = useState<IntakeBody>(blank)
  const [file, setFile] = useState<File | null>(null)
  const [done, setDone] = useState<KeyedAnswer | null>(null)
  const [keyIn, keying] = useKeyInMutation()
  const dispatch = useDispatch()
  const code = f.service.code.trim()
  const { data: policies } = useIntakeFormQuery({ system: f.service.system, code }, { skip: code.length < 4 })
  const questions = new Map<string, string>()
  for (const p of policies ?? []) for (const q of p.questions) if (!questions.has(q.linkId)) questions.set(q.linkId, q.text)
  const questionnaire = policies?.find((p) => p.questionnaire)?.questionnaire
  const set = (patch: Partial<IntakeBody>) => setF({ ...f, ...patch })

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const body: IntakeBody = { ...f, questionnaire, faxes: fax ? [fax] : undefined }
    if (!questionnaire) delete body.answers
    if (file) {
      const data = await new Promise<string>((ok) => {
        const r = new FileReader()
        r.onload = () => ok(String(r.result).replace(/^data:[^,]*,/, ''))
        r.readAsDataURL(file)
      })
      body.documents = [{ description: `${file.name}, received by ${f.channel}`, contentType: file.type || 'application/pdf', data }]
    }
    const r = await keyIn(body)
    if (!('error' in r)) {
      setDone(r.data)
      setF(blank())
      setFile(null)
      onDone()
    }
  }

  return (
    <form onSubmit={submit} aria-labelledby="key-h" className="grid gap-3">
      <h3 id="key-h">Key in a request{fax ? ' from the fax' : ''}</h3>
      {done && (
        <div className={`note ${done.decision === 'approve' ? 'ok' : done.decision === 'deny' ? 'warn' : ''}`} role="status">
          <p>
            <span className={`badge ${done.decision}`}>{decisionText[done.decision]}</span> {done.why}
          </p>
          <p>
            <a
              href={`#/case/${done.case}`}
              onClick={(e) => {
                e.preventDefault()
                dispatch(go({ screen: 'case', id: done.case }))
              }}
            >
              Open the case
            </a>{' '}
            <span className="meta">for its letter to the provider{done.receipt ? ` and receipt ${done.receipt}` : ''}.</span>
          </p>
        </div>
      )}
      <fieldset className="grid gap-2 sm:grid-cols-3">
        <legend>How it arrived</legend>
        <label>
          Channel
          <select value={f.channel} onChange={(e) => set({ channel: e.target.value as Channel })}>
            {channels.map((c) => (
              <option key={c.value} value={c.value}>
                {c.label}
              </option>
            ))}
          </select>
        </label>
        <label>
          Its reference
          <input value={f.reference ?? ''} onChange={(e) => set({ reference: e.target.value })} placeholder="Fax or portal number" />
        </label>
        <label>
          Priority
          <select value={f.priority} onChange={(e) => set({ priority: e.target.value as '' | 'stat' })}>
            <option value="">Standard</option>
            <option value="stat">Expedited</option>
          </select>
        </label>
      </fieldset>
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
        <label>
          Sex
          <select value={f.patient.gender} onChange={(e) => set({ patient: { ...f.patient, gender: e.target.value } })}>
            <option value="">Not shown</option>
            <option value="female">Female</option>
            <option value="male">Male</option>
            <option value="other">Other</option>
            <option value="unknown">Unknown</option>
          </select>
        </label>
        <label>
          State
          <input
            value={f.patient.state}
            maxLength={2}
            onChange={(e) => set({ patient: { ...f.patient, state: e.target.value.toUpperCase() } })}
            placeholder="WA"
          />
        </label>
        <label>
          Coverage
          <select value={f.coverage.type} onChange={(e) => set({ coverage: { type: e.target.value } })}>
            {coverages.map((c) => (
              <option key={c.value} value={c.value}>
                {c.label}
              </option>
            ))}
          </select>
        </label>
      </fieldset>
      <fieldset className="grid gap-2 sm:grid-cols-3">
        <legend>Provider and service</legend>
        <label>
          Provider
          <input required value={f.provider.name} onChange={(e) => set({ provider: { ...f.provider, name: e.target.value } })} />
        </label>
        <label>
          NPI
          <input value={f.provider.npi} inputMode="numeric" maxLength={10} onChange={(e) => set({ provider: { ...f.provider, npi: e.target.value } })} />
        </label>
        <label>
          Service
          <span className="flex gap-1">
            <select aria-label="Code system" value={f.service.system} onChange={(e) => set({ service: { ...f.service, system: e.target.value } })}>
              <option value="cpt">CPT</option>
              <option value="hcpcs">HCPCS</option>
              <option value="rxnorm">RxNorm</option>
            </select>
            <input aria-label="Code" required value={f.service.code} onChange={(e) => set({ service: { ...f.service, code: e.target.value.trim() } })} />
          </span>
        </label>
      </fieldset>
      {code.length >= 4 && policies && policies.length === 0 && (
        <p className="meta">No policy decides this service: Vaglio would decide nothing, so it is not keyed in here.</p>
      )}
      {questions.size > 0 && (
        <fieldset>
          <legend>
            What the request shows <span className="meta">({policies?.map((p) => p.title).join('; ')})</span>
          </legend>
          {[...questions].map(([id, text]) => (
            <div key={id} className="flex flex-wrap items-center justify-between gap-2 border-b border-line py-1.5" role="radiogroup" aria-label={text}>
              <span>{text}</span>
              <span className="seg">
                {(['yes', 'no', ''] as const).map((v) => (
                  <label key={v || 'none'} className={(f.answers?.[id] ?? '') === v ? 'on' : undefined}>
                    <input
                      type="radio"
                      name={id}
                      value={v}
                      checked={(f.answers?.[id] ?? '') === v}
                      onChange={() => set({ answers: { ...f.answers, [id]: v } })}
                    />
                    {v === 'yes' ? 'Yes' : v === 'no' ? 'No' : 'Not shown'}
                  </label>
                ))}
              </span>
            </div>
          ))}
        </fieldset>
      )}
      {!fax && (
        <label>
          What arrived, if not a fax in the inbox <span className="meta">(PDF, TIFF, PNG, JPEG or text, up to 8 MB)</span>
          <input type="file" accept="application/pdf,image/tiff,image/png,image/jpeg,text/plain" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
        </label>
      )}
      <p>
        {viewing && <p className="note">Public demonstration: the form is shown as a clerk sees it; keying in is for a reviewer.</p>}
        <button type="submit" disabled={viewing || keying.isLoading || !policies?.length}>
          {keying.isLoading ? 'Deciding…' : 'Decide it'}
        </button>
      </p>
      {keying.error && <p role="alert">{errorText(keying.error, 'Not decided.')}</p>}
    </form>
  )
}
