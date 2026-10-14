import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useDispatch, useSelector } from 'react-redux'
import { CaseView, Cases } from './Cases'
import { errorText, isUnauthorized } from './common'
import { DraftView, Drafts } from './Editor'
import { CommandPalette, type Command } from './CommandPalette'
import {
  IconAsk,
  IconCases,
  IconChart,
  IconCheck,
  IconDraft,
  IconFlask,
  IconLedger,
  IconLock,
  IconLogout,
  IconPolicies,
  IconSearch,
  IconTheme,
  Mark,
} from './Icons'
import { ReceiptPage, receiptFromHash } from './Receipt'
import { Reports } from './Reports'
import { Audit, Policies, PolicyView, Try } from './Screens'
import {
  api,
  go,
  parseHash,
  toHash,
  useLoginMutation,
  useCasesQuery,
  useLogoutMutation,
  useMeQuery,
  usePoliciesQuery,
  useSsoQuery,
  useDemoQuery,
  type RootState,
  type Screen,
} from './store'
import { THEMES, THEME_LABELS, useTheme, type Theme } from './Theme'
import { can, type Me } from './types'

export { Result } from './common'

export function App() {
  const [hash, setHash] = useState(window.location.hash)
  useEffect(() => {
    const on = () => setHash(window.location.hash)
    window.addEventListener('hashchange', on)
    return () => window.removeEventListener('hashchange', on)
  }, [])
  const receipt = receiptFromHash(hash)
  // The receipt page is public: whoever holds a receipt checks it without an account.
  if (receipt !== null) return <ReceiptPage code={receipt} />
  return <Signed />
}

function Signed() {
  const { data: me, error, isLoading } = useMeQuery()
  if (isLoading) return <p className="layout text-muted">Loading…</p>
  if (isUnauthorized(error) || !me) return <SignIn />
  return <Console me={me} />
}

function SignIn() {
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [login, { error, isLoading }] = useLoginMutation()
  const { data: sso } = useSsoQuery()
  const { data: demo } = useDemoQuery()
  const ssoResult = /sso=(failed|unknown)/.exec(window.location.hash)?.[1]
  function submit(e: FormEvent) {
    e.preventDefault()
    login({ name: name.trim(), password })
  }
  return (
    <div className="layout signin">
      <div className="grid w-full max-w-4xl items-center gap-10 md:grid-cols-[1.1fr_1fr]">
        <header className="text-center md:text-left">
          <div className="flex justify-center md:justify-start">
            <span className="rounded-[1.4rem] shadow-[0_20px_60px_-15px_rgba(168,85,247,0.7)]">
              <Mark size={88} />
            </span>
          </div>
          <h1 className="mt-6 text-5xl font-extrabold tracking-tight">
            <span className="bg-gradient-to-r from-fuchsia-300 via-violet-300 to-violet-500 bg-clip-text text-transparent">
              Vaglio
            </span>
          </h1>
          <p className="tagline mt-3 text-lg">Prior authorisation decisions that cannot deny care without proving why.</p>
          <ul className="mt-6 hidden space-y-2 text-sm text-muted md:block" aria-label="What it promises">
            <li className="flex items-center gap-2">
              <IconCheck className="text-ok" /> Every decision cites the plan&apos;s policy and the record
            </li>
            <li className="flex items-center gap-2">
              <IconAsk className="text-due" /> Missing evidence is asked for, never held against the patient
            </li>
            <li className="flex items-center gap-2">
              <IconLock className="text-accent" /> Every step kept in a tamper-evident log
            </li>
          </ul>
        </header>
        <main>
          <div className="panel">
            {ssoResult && (
              <p role="alert" className="note warn">
                {ssoResult === 'unknown'
                  ? 'Your organization signed you in, but there is no account here for you to use with it. An administrator adds one.'
                  : 'Single sign-on did not complete. Try again; the audit log says why.'}
              </p>
            )}
            {sso?.enabled && (
              <p>
                <a className="button w-full justify-center" href="/api/sso/start">
                  {sso.label}
                </a>
              </p>
            )}
            <form onSubmit={submit} aria-labelledby="signin-h">
              <h2 id="signin-h" className="m-0 text-xl font-bold">
                {sso?.enabled ? 'Or sign in with a password' : 'Sign in'}
              </h2>
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
              {demo && (
                <div className="note mt-4 text-sm">
                  <strong>A public demonstration</strong>, on invented requests only. Sign in as{' '}
                  <code>{demo.name}</code> with <code>{demo.password}</code>{' '}
                  <button
                    type="button"
                    className="link"
                    onClick={() => {
                      setName(demo.name)
                      setPassword(demo.password)
                    }}
                  >
                    fill it in
                  </button>
                  . It can look at everything and change nothing.
                </div>
              )}
              <button type="submit" disabled={isLoading} className="mt-5 w-full justify-center">
                {isLoading ? 'Signing in…' : 'Sign in'}
              </button>
              {error && <p role="alert">{errorText(error, 'Could not sign in.')}</p>}
            </form>
          </div>
        </main>
      </div>
    </div>
  )
}

type Tab = { screen: Screen; label: string; icon: (p: { size?: number; className?: string }) => JSX.Element; tone: string; shown?: (me: Me) => boolean }

const tabs: Tab[] = [
  { screen: 'cases', label: 'Cases', icon: IconCases, tone: 'text-amber-400' },
  { screen: 'policies', label: 'Policies', icon: IconPolicies, tone: 'text-violet-400' },
  {
    screen: 'drafts',
    label: 'Drafts',
    icon: IconDraft,
    tone: 'text-fuchsia-400',
    shown: (me) => me.editing && (can(me, 'admin', 'author') || !!me.npi),
  },
  { screen: 'try', label: 'Try a policy', icon: IconFlask, tone: 'text-sky-400' },
  { screen: 'reports', label: 'Reports', icon: IconChart, tone: 'text-emerald-400' },
  { screen: 'audit', label: 'Audit log', icon: IconLedger, tone: 'text-rose-400', shown: (me) => can(me, 'admin', 'audit') },
]

function Console({ me }: { me: Me }) {
  const view = useSelector((s: RootState) => s.view)
  const dispatch = useDispatch()
  const [logout] = useLogoutMutation()
  const [palette, setPalette] = useState(false)
  const { theme, setTheme } = useTheme()

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
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPalette((o) => !o)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const shown = tabs.filter((t) => !t.shown || t.shown(me))
  const commands = useCommands(shown, palette)
  const section =
    view.screen === 'case' ? 'cases' : view.screen === 'policy' ? 'policies' : view.screen === 'draft' ? 'drafts' : view.screen
  return (
    <>
      {me.demo && (
        <div className="demo-banner" role="note">
          Public demonstration: every request here is invented. Nothing is about a real person. Viewing only.
        </div>
      )}
      <header className="bar sticky top-0 z-40 border-b border-line bg-page/70 backdrop-blur-xl">
        <div className="mx-auto flex max-w-[76rem] flex-wrap items-center gap-x-6 gap-y-3 px-6 py-3">
          <a
            href="#/cases"
            className="flex items-center gap-3 no-underline hover:no-underline"
            onClick={(e) => {
              e.preventDefault()
              dispatch(go({ screen: 'cases', id: null }))
            }}
          >
            <Mark size={38} />
            <span className="leading-tight">
              <h1 className="m-0 text-xl font-extrabold tracking-tight text-ink">Vaglio</h1>
              <span className="block text-[11px] font-medium uppercase tracking-[0.14em] text-faint">Prior authorization</span>
            </span>
          </a>
          <nav aria-label="Sections" className="flex flex-wrap items-center gap-1">
            {shown.map((t) => {
              const on = section === t.screen
              const I = t.icon
              return (
                <a
                  key={t.screen}
                  href={toHash({ screen: t.screen, id: null })}
                  aria-current={on ? 'page' : undefined}
                  className={`flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm font-semibold no-underline transition hover:no-underline ${
                    on ? 'bg-raised text-ink shadow-sm ring-1 ring-line' : 'text-muted hover:bg-raised/60 hover:text-ink'
                  }`}
                  onClick={(e) => {
                    e.preventDefault()
                    dispatch(go({ screen: t.screen, id: null }))
                  }}
                >
                  <I size={16} className={t.tone} />
                  {t.label}
                </a>
              )
            })}
          </nav>
          <div className="ml-auto flex items-center gap-2">
            <button
              type="button"
              onClick={() => setPalette(true)}
              aria-label="Search (Command K)"
              className="m-0 border border-line bg-raised px-3 py-1.5 text-sm font-medium text-muted shadow-none hover:text-ink"
            >
              <IconSearch size={15} /> <span className="hidden xl:inline">Search</span>
              <kbd className="ml-1 rounded border border-line px-1.5 text-[10px] text-faint">⌘K</kbd>
            </button>
            <label className="m-0 flex items-center gap-1 text-faint" title="Theme">
              <IconTheme size={15} />
              <span className="sr-only">Theme</span>
              <select
                value={theme}
                onChange={(e) => setTheme(e.target.value as Theme)}
                className="w-auto border-line bg-raised py-1 pl-2 pr-7 text-sm text-ink"
              >
                {THEMES.map((t) => (
                  <option key={t} value={t} title={THEME_LABELS[t].about}>
                    {THEME_LABELS[t].label}
                  </option>
                ))}
              </select>
            </label>
          <span className="who flex items-center gap-2 text-sm">
            <span
              aria-hidden="true"
              title={me.user}
              className="grid h-8 w-8 place-items-center rounded-full bg-gradient-to-br from-violet-500 to-fuchsia-600 text-xs font-bold text-white"
            >
              {me.user.slice(0, 2).toUpperCase()}
            </span>
            <span className="hidden font-semibold 2xl:inline" title={`${me.role}${me.npi ? `, clinician, NPI ${me.npi}` : ''}`}>
              {me.user}
            </span>{' '}
            <span className="meta hidden 2xl:inline">
              ({me.role}
              {me.npi ? `, clinician, NPI ${me.npi}` : ''}
              {me.scopes?.length ? `; ${me.scopes.join(', ')}` : ''})
            </span>{' '}
            <button
              type="button"
              className="link flex items-center gap-1"
              title="Sign out"
              onClick={async () => {
                await logout()
                dispatch(api.util.resetApiState())
              }}
            >
              <IconLogout size={15} />
              <span className="sr-only 2xl:not-sr-only">Sign out</span>
            </button>
          </span>
          </div>
        </div>
      </header>
      <CommandPalette commands={commands} open={palette} onClose={() => setPalette(false)} />
      <div className="layout">
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
        <footer className="mt-10 text-center text-xs text-faint">
          Vaglio · source-available under PolyForm Shield 1.0.0 · BioDream LLC
        </footer>
      </div>
    </>
  )
}

/** What the palette offers: every screen, every policy and the latest cases. Fetched only while it is open. */
function useCommands(shown: Tab[], open: boolean): Command[] {
  const dispatch = useDispatch()
  const { data: policies } = usePoliciesQuery(undefined, { skip: !open })
  const { data: cases } = useCasesQuery('', { skip: !open })
  return useMemo(() => {
    const out: Command[] = shown.map((t) => ({
      id: 'screen:' + t.screen,
      label: t.label,
      group: 'Screens',
      run: () => dispatch(go({ screen: t.screen, id: null })),
    }))
    for (const p of policies ?? []) {
      out.push({
        id: 'policy:' + p.id,
        label: p.title,
        group: 'Policies',
        hint: 'v' + p.version,
        keywords: [p.id, ...p.services.map((s) => s.code)],
        run: () => dispatch(go({ screen: 'policy', id: p.id })),
      })
    }
    for (const c of (cases ?? []).slice(0, 50)) {
      out.push({
        id: 'case:' + c.id,
        label: `${c.policyTitle || c.policy} · ${c.service.slice(c.service.lastIndexOf('|') + 1)}`,
        group: 'Cases',
        hint: c.status === 'pended' ? 'waiting' : c.decision,
        keywords: [c.claim, c.patient, c.id],
        run: () => dispatch(go({ screen: 'case', id: c.id })),
      })
    }
    return out
  }, [shown, policies, cases, dispatch])
}
