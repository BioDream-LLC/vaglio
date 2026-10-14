import { FromDocument } from './FromDocument'
import { useEffect, useRef, useState } from 'react'
import { useDispatch } from 'react-redux'
import { errorText } from './common'
import {
  go,
  useCheckSourceMutation,
  useDraftQuery,
  useDraftsQuery,
  usePoliciesQuery,
  useRemoveDraftMutation,
  useSaveDraftMutation,
  useSignOffMutation,
} from './store'
import { can, type Me, type SourceCheck } from './types'

/** A blank policy to start from: every block the language has, with the rules a draft must meet said in comments. */
export const starter = (id: string) => `# Draft. Quote each clause word for word from the source document; say a draft's
# source in the source line. A clinician signs it off before it decides anything.
policy ${id}
  title "A title"
  version 0.1.0
  source "The coverage document, its section"
  status draft
  questionnaire https://payer.example/Questionnaire/${id.replace(/_/g, '-')}
  service cpt 00000

evidence an_indication
  text "What the evidence is"
  clause "The sentence of the source it encodes."
  find question an-indication

criterion indication_unestablished
  text "The indication is not documented."
  clause "The sentence of the source it encodes."
  defeated by an_indication

decision approve approves
  text "Approve."
  clause "The sentence of the source it encodes."
  defeated by indication_unestablished

decision deny denies
  text "Deny: the indication is not documented."
  clause "The sentence of the source it encodes."
  defeated by an_indication
`

/** The drafts, and a way to start one. */
export function Drafts({ me }: { me: Me }) {
  const { data: drafts, error } = useDraftsQuery()
  const { data: policies } = usePoliciesQuery()
  const [newId, setNewId] = useState('')
  const [save, saving] = useSaveDraftMutation()
  const dispatch = useDispatch()
  const author = can(me, 'admin', 'author')
  const valid = /^[a-z][a-z0-9_]{1,63}$/.test(newId)
  const taken = !!policies?.some((p) => p.id === newId) || !!drafts?.some((d) => d.id === newId)
  return (
    <section aria-labelledby="drafts-h">
      <h2 id="drafts-h">Policy drafts</h2>
      <p className="meta">
        A draft decides nothing. Save it as often as you like; it is checked as you write. When it passes, a clinician
        signs it off, and it is in force at once, replacing the version before it. Every save and sign-off is in the
        audit log.
      </p>
      {error && <p role="alert">{errorText(error, 'Could not load the drafts.')}</p>}
      {drafts && drafts.length === 0 && <p>No drafts.</p>}
      {drafts && drafts.length > 0 && (
        <table className="cases">
          <thead>
            <tr>
              <th scope="col">Policy</th>
              <th scope="col">Saved</th>
              <th scope="col">State</th>
            </tr>
          </thead>
          <tbody>
            {drafts.map((d) => (
              <tr key={d.id}>
                <td>
                  <a
                    href={`#/draft/${encodeURIComponent(d.id)}`}
                    onClick={(e) => {
                      e.preventDefault()
                      dispatch(go({ screen: 'draft', id: d.id }))
                    }}
                  >
                    {d.id}
                  </a>
                  {d.policy && <span className="meta"> v{d.policy.version}</span>}
                  {d.inForce && <span className="meta"> (replaces v{d.inForce})</span>}
                </td>
                <td>{new Date(d.saved).toLocaleString()}</td>
                <td>
                  <State c={d} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {author && (
        <form
          className="inline"
          onSubmit={async (e) => {
            e.preventDefault()
            const r = await save({ id: newId, source: starter(newId) })
            if ('data' in r) dispatch(go({ screen: 'draft', id: newId }))
          }}
        >
          <label htmlFor="new-id">New policy id</label>{' '}
          <input id="new-id" value={newId} onChange={(e) => setNewId(e.target.value.trim())} placeholder="knee_mri" />{' '}
          <button type="submit" disabled={!valid || taken || saving.isLoading}>
            Start a draft
          </button>
          {newId && !valid && <span className="meta"> lower case letters, digits and _, starting with a letter</span>}
          {taken && <span className="meta"> taken: open it, or edit the policy in force from its page</span>}
          {saving.error && <p role="alert">{errorText(saving.error, 'Not saved.')}</p>}
        </form>
      )}
      {author && <FromDocument />}
    </section>
  )
}

function State({ c }: { c: SourceCheck }) {
  if (c.error) return <span className="badge deny">does not parse</span>
  if (c.ready) return <span className="badge approve">ready to sign</span>
  if (c.check && !c.check.ok) return <span className="badge deny">fails its check</span>
  return <span className="badge pend">not ready</span>
}

/** One draft: the source, checked as it is written, saved by the author, signed off by a clinician. */
export function DraftView({ id, me }: { id: string; me: Me }) {
  const { data: d, error } = useDraftQuery(id)
  const [text, setText] = useState<string | null>(null)
  const [check, checked] = useCheckSourceMutation()
  const [save, saving] = useSaveDraftMutation()
  const [remove, removing] = useRemoveDraftMutation()
  const [sign, signing] = useSignOffMutation()
  const [confirming, setConfirming] = useState(false)
  const dispatch = useDispatch()
  const area = useRef<HTMLTextAreaElement>(null)
  const author = can(me, 'admin', 'author')
  const source = text ?? d?.source ?? ''
  const dirty = d !== undefined && text !== null && text !== d.source

  // Check as the author writes, a moment after they stop.
  useEffect(() => {
    if (text === null) return
    const t = setTimeout(() => check(text), 400)
    return () => clearTimeout(t)
  }, [text, check])

  // Leaving with unsaved work asks first.
  useEffect(() => {
    if (!dirty) return
    const stop = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', stop)
    return () => window.removeEventListener('beforeunload', stop)
  }, [dirty])

  if (error) return <p role="alert">{errorText(error, 'Could not load the draft.')}</p>
  if (!d) return <p>Loading…</p>
  const c: SourceCheck = text !== null && checked.data ? checked.data : d
  const where = (line?: number, col?: number) => {
    const el = area.current
    if (!el || !line) return
    const lines = source.split('\n')
    let at = 0
    for (let i = 0; i < line - 1 && i < lines.length; i++) at += lines[i].length + 1
    at += Math.max(0, (col ?? 1) - 1)
    el.focus()
    el.setSelectionRange(at, at)
  }
  return (
    <section aria-labelledby="draft-h">
      <p>
        <a
          href="#/drafts"
          onClick={(e) => {
            e.preventDefault()
            dispatch(go({ screen: 'drafts', id: null }))
          }}
        >
          ← Drafts
        </a>
      </p>
      <h2 id="draft-h">
        Draft: {id} {c.policy && <span className="meta">v{c.policy.version}</span>}
      </h2>
      {d.inForce && (
        <p className="meta">
          Version {d.inForce} is in force and keeps deciding until this draft is signed off.
        </p>
      )}
      <label htmlFor="draft-source">Source</label>
      <textarea
        id="draft-source"
        ref={area}
        className="source-code"
        spellCheck={false}
        readOnly={!author}
        aria-describedby="draft-check"
        rows={Math.min(48, source.split('\n').length + 2)}
        value={source}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === 's') {
            e.preventDefault()
            if (author && dirty) save({ id, source })
          }
        }}
      />
      <div id="draft-check" aria-live="polite">
        <CheckPanel c={c} goTo={where} />
      </div>
      <p>
        {author && (
          <button type="button" disabled={!dirty || saving.isLoading} onClick={() => save({ id, source })}>
            {saving.isLoading ? 'Saving…' : dirty ? 'Save draft' : 'Saved'}
          </button>
        )}{' '}
        {author && (
          <button
            type="button"
            className="link"
            disabled={removing.isLoading}
            onClick={async () => {
              if (!window.confirm(`Remove the draft of ${id}? The policy in force, if any, is not touched.`)) return
              const r = await remove(id)
              if (!('error' in r)) dispatch(go({ screen: 'drafts', id: null }))
            }}
          >
            Remove draft
          </button>
        )}
      </p>
      {saving.error && <p role="alert">{errorText(saving.error, 'Not saved.')}</p>}
      {removing.error && <p role="alert">{errorText(removing.error, 'Not removed.')}</p>}

      <h3>Sign-off</h3>
      {!me.npi ? (
        <p className="meta">
          A clinician signs a policy off: an account recorded with an NPI and a credential. The signature is the
          account's name and credential, never typed.
        </p>
      ) : dirty ? (
        <p className="meta">Save the draft first: what is signed is what is saved.</p>
      ) : !d.ready ? (
        <p className="meta">Not ready to sign: {d.error ?? d.notReady}</p>
      ) : !confirming ? (
        <button type="button" onClick={() => setConfirming(true)}>
          Sign off and put in force…
        </button>
      ) : (
        <div className="note warn">
          <p>
            You sign that you have read every rule of {id} v{d.policy?.version} against its source, as {me.user},{' '}
            {me.credential}, NPI {me.npi}. It decides requests from now on
            {d.inForce ? `, in place of v${d.inForce}` : ''}.
          </p>
          <button
            type="button"
            disabled={signing.isLoading}
            onClick={async () => {
              const r = await sign(id)
              if (!('error' in r)) dispatch(go({ screen: 'policy', id }))
            }}
          >
            {signing.isLoading ? 'Signing…' : 'Sign off'}
          </button>{' '}
          <button type="button" className="link" onClick={() => setConfirming(false)}>
            Cancel
          </button>
          {signing.error && <p role="alert">{errorText(signing.error, 'Not signed.')}</p>}
        </div>
      )}
    </section>
  )
}

function CheckPanel({ c, goTo }: { c: SourceCheck; goTo: (line?: number, col?: number) => void }) {
  if (c.error)
    return (
      <p className="note warn">
        {c.line ? (
          <button type="button" className="link" onClick={() => goTo(c.line, c.col)}>
            Line {c.line}, column {c.col}
          </button>
        ) : null}
        {c.line ? ': ' : ''}
        {c.error.replace(/^[^:]+:\d+:\d+: /, '')}
      </p>
    )
  if (!c.check) return null
  const k = c.check
  return (
    <div className={k.ok && k.warnings.length <= 1 ? 'note ok' : 'note warn'}>
      <p>
        {k.ok ? 'Passes' : 'Fails'} its check over all {k.combinations} combinations of evidence:{' '}
        {Object.entries(k.outcomes)
          .map(([o, n]) => `${o.replace('/decided', '')} ${n}`)
          .join(', ')}
        .
      </p>
      {(k.defects?.length ?? 0) > 0 && (
        <p>
          It decides wrongly with: {k.defects!.slice(0, 3).map((d) => (d.length ? d.join(', ') : 'no evidence')).join('; ')}
          {k.defects!.length > 3 ? `, and ${k.defects!.length - 3} more` : ''}.
        </p>
      )}
      {(k.unreachable?.length ?? 0) > 0 && <p>Never reached: {k.unreachable!.join(', ')}.</p>}
      {k.warnings.length > 0 && (
        <ul>
          {k.warnings.map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      )}
      {!c.ready && c.notReady && !c.notReady.startsWith('it fails its check') && <p className="meta">{c.notReady}</p>}
    </div>
  )
}
