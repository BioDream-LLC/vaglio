import { useEffect, useState, type FormEvent } from 'react'
import { useDispatch, useSelector } from 'react-redux'
import { CaseView, Cases } from './Cases'
import { errorText, isUnauthorized } from './common'
import { DraftView, Drafts } from './Editor'
import { Reports } from './Reports'
import { Audit, Policies, PolicyView, Try } from './Screens'
import {
  api,
  go,
  parseHash,
  toHash,
  useLoginMutation,
  useLogoutMutation,
  useMeQuery,
  useSsoQuery,
  type RootState,
  type Screen,
} from './store'
import { can, type Me } from './types'

export { Result } from './common'

export function App() {
  const { data: me, error, isLoading } = useMeQuery()
  if (isLoading) return <p className="layout">Loading…</p>
  if (isUnauthorized(error) || !me) return <SignIn />
  return <Console me={me} />
}

function SignIn() {
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [login, { error, isLoading }] = useLoginMutation()
  const { data: sso } = useSsoQuery()
  const ssoResult = /sso=(failed|unknown)/.exec(window.location.hash)?.[1]
  function submit(e: FormEvent) {
    e.preventDefault()
    login({ name: name.trim(), password })
  }
  return (
    <div className="layout signin">
      <header>
        <h1>Vaglio</h1>
        <p className="tagline">Prior authorisation decisions that cannot deny care without proving why.</p>
      </header>
      <main>
        {ssoResult && (
          <p role="alert" className="note warn">
            {ssoResult === 'unknown'
              ? 'Your organization signed you in, but there is no account here for you to use with it. An administrator adds one.'
              : 'Single sign-on did not complete. Try again; the audit log says why.'}
          </p>
        )}
        {sso?.enabled && (
          <p>
            <a className="button" href="/api/sso/start">
              {sso.label}
            </a>
          </p>
        )}
        <form onSubmit={submit} aria-labelledby="signin-h">
          <h2 id="signin-h">{sso?.enabled ? 'Or sign in with a password' : 'Sign in'}</h2>
          <label htmlFor="name">Name</label>
          <input id="name" autoComplete="username" required value={name} onChange={(e) => setName(e.target.value)} />
          <label htmlFor="password">Password</label>
          <input
            id="password"
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          <button type="submit" disabled={isLoading}>
            {isLoading ? 'Signing in…' : 'Sign in'}
          </button>
          {error && <p role="alert">{errorText(error, 'Could not sign in.')}</p>}
        </form>
      </main>
    </div>
  )
}

const tabs: { screen: Screen; label: string; shown?: (me: Me) => boolean }[] = [
  { screen: 'cases', label: 'Cases' },
  { screen: 'policies', label: 'Policies' },
  { screen: 'drafts', label: 'Drafts', shown: (me) => me.editing && (can(me, 'admin', 'author') || !!me.npi) },
  { screen: 'try', label: 'Try a policy' },
  { screen: 'reports', label: 'Reports' },
  { screen: 'audit', label: 'Audit log', shown: (me) => can(me, 'admin', 'audit') },
]

function Console({ me }: { me: Me }) {
  const view = useSelector((s: RootState) => s.view)
  const dispatch = useDispatch()
  const [logout] = useLogoutMutation()

  // The hash is the view: follow it on load and on back/forward, and keep it in step when the view changes.
  useEffect(() => {
    const follow = () => dispatch(go(parseHash(window.location.hash)))
    follow()
    window.addEventListener('hashchange', follow)
    return () => window.removeEventListener('hashchange', follow)
  }, [dispatch])
  useEffect(() => {
    const h = toHash(view)
    if (window.location.hash !== h) window.history.pushState(null, '', h)
  }, [view])

  const section =
    view.screen === 'case' ? 'cases' : view.screen === 'policy' ? 'policies' : view.screen === 'draft' ? 'drafts' : view.screen
  return (
    <div className="layout">
      <header className="bar">
        <h1>Vaglio</h1>
        <nav aria-label="Sections">
          {tabs
            .filter((t) => !t.shown || t.shown(me))
            .map((t) => (
              <a
                key={t.screen}
                href={toHash({ screen: t.screen, id: null })}
                aria-current={section === t.screen ? 'page' : undefined}
                onClick={(e) => {
                  e.preventDefault()
                  dispatch(go({ screen: t.screen, id: null }))
                }}
              >
                {t.label}
              </a>
            ))}
        </nav>
        <span className="who">
          {me.user}{' '}
          <span className="meta">
            ({me.role}
            {me.npi ? `, clinician, NPI ${me.npi}` : ''}
            {me.scopes?.length ? `; ${me.scopes.join(', ')}` : ''})
          </span>{' '}
          <button
            type="button"
            className="link"
            onClick={async () => {
              await logout()
              dispatch(api.util.resetApiState())
            }}
          >
            Sign out
          </button>
        </span>
      </header>
      <main>
        {view.screen === 'cases' && <Cases />}
        {view.screen === 'case' && view.id && <CaseView id={view.id} me={me} />}
        {view.screen === 'policies' && <Policies />}
        {view.screen === 'policy' && view.id && <PolicyView id={view.id} me={me} />}
        {view.screen === 'reports' && <Reports />}
        {view.screen === 'try' && <Try />}
        {view.screen === 'drafts' && (me.editing ? <Drafts me={me} /> : <p>The policy editor is off on this server.</p>)}
        {view.screen === 'draft' && view.id && <DraftView id={view.id} me={me} />}
        {view.screen === 'audit' &&
          (can(me, 'admin', 'audit') ? <Audit /> : <p>The audit log needs the admin role or the audit scope.</p>)}
      </main>
    </div>
  )
}
