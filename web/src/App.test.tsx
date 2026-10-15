import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { Provider } from 'react-redux'
import { makeStore, parseHash, selectPolicy, setEvidence, toHash, toRequest } from './store'
import { App, Result } from './App'

describe('toRequest', () => {
  it('sends only addressed evidence, trimmed', () => {
    const store = makeStore()
    store.dispatch(selectPolicy('lumbar_mri'))
    store.dispatch(setEvidence({ argument: 'red_flag', patch: { finding: 'absent', reference: ' Doc/1 ', quote: ' normal exam ' } }))
    store.dispatch(setEvidence({ argument: 'surgical_planning', patch: { finding: 'present' } }))
    store.dispatch(setEvidence({ argument: 'surgical_planning', patch: { finding: 'unaddressed' } }))
    expect(toRequest(store.getState().request)).toEqual({
      policy: 'lumbar_mri',
      evidence: [{ argument: 'red_flag', finding: 'absent', reference: 'Doc/1', quote: 'normal exam' }],
    })
  })

  it('clears evidence when the policy changes', () => {
    const store = makeStore()
    store.dispatch(selectPolicy('a'))
    store.dispatch(setEvidence({ argument: 'x', patch: { finding: 'present' } }))
    store.dispatch(selectPolicy('b'))
    expect(store.getState().request.evidence).toEqual({})
  })
})

describe('Result', () => {
  it('says what to ask for when a denial is pended', () => {
    render(
      <Result
        result={{
          policy: 'lumbar_mri',
          policyVersion: '0.1.0',
          outcome: 'pended',
          reason: 'needs_information',
          missing: [{ argument: 'red_flag', text: 'A red-flag finding is documented.', clause: 'c' }],
          labels: {},
        }}
      />,
    )
    expect(screen.getByRole('heading', { name: 'Pended' })).toBeTruthy()
    expect(screen.getByRole('heading', { name: 'Information needed' })).toBeTruthy()
    expect(screen.getByText('A red-flag finding is documented.')).toBeTruthy()
  })
})

describe('navigation', () => {
  it('reads and writes the hash', () => {
    expect(parseHash('#/case/abc')).toEqual({ screen: 'case', id: 'abc' })
    expect(parseHash('#/case')).toEqual({ screen: 'cases', id: null })
    expect(parseHash('#/nonsense')).toEqual({ screen: 'cases', id: null })
    expect(parseHash('')).toEqual({ screen: 'cases', id: null })
    expect(toHash({ screen: 'policy', id: 'lumbar mri' })).toBe('#/policy/lumbar%20mri')
  })
})

function respond(routes: Record<string, unknown>) {
  vi.stubGlobal('fetch', async (input: Request | string) => {
    const url = typeof input === 'string' ? input : input.url
    const path = new URL(url, 'http://localhost').pathname
    if (!(path in routes)) return new Response(JSON.stringify({ error: 'no' }), { status: 404 })
    const body = routes[path]
    if (body === 401) return new Response(JSON.stringify({ error: 'sign in first' }), { status: 401 })
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  })
}

const pendedCase = {
  id: 'c1', created: '2026-10-01T10:00:00Z', updated: '2026-10-01T10:00:00Z', status: 'pended',
  policy: 'lumbar_mri', policyVersion: '0.1.0', policyTitle: 'MRI of the lumbar spine', item: '1',
  service: 'http://www.ama-assn.org/go/cpt|72148', claim: 'sys|C-1', patient: 'Patient/p1', provider: 'Organization/o',
  request: 'resp-1', bundle: 'abcdef0123456789',
  answer: { decision: 'pend', why: 'A decision needs information.', requested: ['A red-flag finding'] },
  result: { policy: 'lumbar_mri', policyVersion: '0.1.0', outcome: 'pended', reason: 'needs_information', labels: {} },
  evidence: { findings: [], contradictions: [], ignored: [], unread: [{ reference: 'DocumentReference/d1', what: 'a document: Clinic note' }] },
  history: [{ seq: 1, time: '2026-10-01T10:00:00Z', actor: 'pas', action: 'case.opened', case: 'c1', prev: '', hash: 'h' }],
}

function renderApp() {
  return render(
    <Provider store={makeStore()}>
      <App />
    </Provider>,
  )
}

describe('App', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    window.location.hash = ''
  })

  it('asks a signed-out user to sign in', async () => {
    respond({ '/api/me': 401 })
    renderApp()
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeTruthy()
    expect(screen.getByLabelText('Password')).toBeTruthy()
  })

  it('lets a reviewer decide a pended case, and shows the unread documents', async () => {
    window.location.hash = '#/case/c1'
    respond({ '/api/me': { user: 'dr.lee', role: 'reviewer' }, '/api/cases/c1': pendedCase })
    renderApp()
    expect(await screen.findByRole('heading', { name: 'Your decision' })).toBeTruthy()
    expect(screen.getByText('A red-flag finding')).toBeTruthy()
    expect(screen.getByText('a document: Clinic note')).toBeTruthy()
    expect(screen.queryByText('Audit log')).toBeNull() // not an admin
  })

  it('does not offer a viewer the review form', async () => {
    window.location.hash = '#/case/c1'
    respond({ '/api/me': { user: 'v', role: 'viewer' }, '/api/cases/c1': pendedCase })
    renderApp()
    expect(await screen.findByText('A reviewer decides this case.')).toBeTruthy()
    expect(screen.queryByRole('heading', { name: 'Your decision' })).toBeNull()
  })
})

describe('clinician denials and reports', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    window.location.hash = ''
  })

  const proposed = {
    ...pendedCase,
    answer: { decision: 'pend', why: 'Pended for review by a licensed clinician.' },
    proposed: { decision: 'deny', why: 'Deny: repeat study within six months.' },
  }

  it('shows the proposed denial, and lets only a clinician deny', async () => {
    window.location.hash = '#/case/c1'
    respond({ '/api/me': { user: 'r', role: 'reviewer', clinicianDenials: true }, '/api/cases/c1': proposed })
    renderApp()
    expect(await screen.findByText('The engine proposes a denial.')).toBeTruthy()
    expect(screen.getByText('Deny: repeat study within six months.')).toBeTruthy()
    expect((screen.getByRole('radio', { name: 'Deny' }) as HTMLInputElement).disabled).toBe(true)
    expect((screen.getByRole('radio', { name: 'Approve' }) as HTMLInputElement).disabled).toBe(false)
  })

  it('records a clinician with their own NPI', async () => {
    window.location.hash = '#/case/c1'
    respond({
      '/api/me': { user: 'r', role: 'reviewer', clinicianDenials: true, npi: '1234567893', credential: 'MD, radiology' },
      '/api/cases/c1': proposed,
    })
    renderApp()
    expect(await screen.findByText(/Recorded with your NPI 1234567893/)).toBeTruthy()
    expect((screen.getByRole('radio', { name: 'Deny' }) as HTMLInputElement).disabled).toBe(false)
    expect(screen.queryByLabelText('Your NPI (optional)')).toBeNull()
  })

  it('shows the metrics, with the extension and lateness rows', async () => {
    window.location.hash = '#/reports'
    const g = { requests: 2, approved: 1, denied: 1, pending: 0, approvedPercent: 50, deniedPercent: 50, realTime: 2,
      afterReview: 0, goldCard: 0, averageHours: 0, medianHours: 0, appealed: 0, approvedAfterAppeal: 0,
      approvedAfterAppealPercent: null, extended: 1, approvedAfterExtension: 1, approvedAfterExtensionPercent: 50, late: 1 }
    const none = { ...g, requests: 0, approved: 0, denied: 0, realTime: 0, approvedPercent: null, deniedPercent: null,
      averageHours: null, medianHours: null }
    respond({
      '/api/me': { user: 'v', role: 'viewer' },
      '/api/reports/metrics': { from: '2026-01-01', to: '2026-10-13', standard: g, expedited: none, drugStandard: none,
        drugExpedited: none, services: [],
        superseded: 0, notTracked: [] },
      '/api/reports/pends': { from: '', to: '', forInformation: {}, missing: [], contradicted: [] },
      '/api/goldcard': { enabled: false, programs: [], standings: [] },
      '/api/reports/overrides': { from: '', to: '', by: 'clause', rows: [] },
      '/api/reports/list': { from: '', to: '', criteria: { minRequests: 30, minApprovedPercent: 95, minReversedPercent: 50 },
        note: 'It recommends.', rows: [{ service: 'cpt|72148', code: '72148', policy: 'lumbar_mri', title: 'Lumbar MRI',
          requests: 40, approved: 40, approvedPercent: 100, engineApproved: 39, pended: 1, exempt: 0, denied: 0,
          engineDenials: 0, reversed: 0, reversedPercent: null, waiting: 0, medianReviewHours: 3, verdict: 'consider-removing',
          why: 'approved 40 of 40 (100.0%), never denied: review changed no answer' }] },
    })
    renderApp()
    expect((await screen.findAllByText('1 (50.0%)', undefined, { timeout: 3000 })).length).toBe(2) // approved and denied
    expect(await screen.findByRole('heading', { name: 'Which services still need prior authorization' })).toBeTruthy()
    expect(screen.getByText('Consider removing')).toBeTruthy()
    expect(screen.getByText('Approved after an extension')).toBeTruthy()
    expect(screen.getByText('Determined late, or waiting past the deadline')).toBeTruthy()
    expect(screen.getByText(/No gold card programs are configured/)).toBeTruthy()
  })
})

describe('documents and routes', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    window.location.hash = ''
  })

  it('shows what would approve it, and a reading waiting for confirmation', async () => {
    window.location.hash = '#/case/c1'
    respond({
      '/api/me': { user: 'r', role: 'reviewer' },
      '/api/cases/c1': {
        ...pendedCase,
        result: {
          ...pendedCase.result,
          toApprove: [[{ argument: 'red_flag', text: 'A red-flag finding', clause: 'c' }]],
        },
        readings: [{ document: 'DocumentReference/d1', model: 'local-model', refused: ['x: not in the document'],
          proposals: [{ argument: 'red_flag', text: 'A red-flag finding', finding: 'absent', document: 'DocumentReference/d1',
            quote: 'The neurological examination is normal.' }] }],
      },
      '/api/cases/c1/documents': { reader: true, fetch: true, skipped: [],
        documents: [{ reference: 'DocumentReference/d1', what: 'Clinic note' }] },
    })
    renderApp()
    expect(await screen.findByRole('heading', { name: 'What would approve it' })).toBeTruthy()
    expect(await screen.findByRole('button', { name: 'Read it with the model' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Confirm' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Fetch documents the provider sent since' })).toBeTruthy()
    expect(screen.getByText('“The neurological examination is normal.”')).toBeTruthy()
  })
})

describe('gold card counting', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    window.location.hash = ''
  })

  const z = { requests: 0, approved: 0, denied: 0, pending: 0, approvedPercent: null, deniedPercent: null, realTime: 0,
    afterReview: 0, goldCard: 0, appealed: 0, approvedAfterAppeal: 0, approvedAfterAppealPercent: null, averageHours: null,
    medianHours: null, extended: 0, approvedAfterExtension: 0, approvedAfterExtensionPercent: null, late: 0 }
  const routes = (role: string) => ({
    '/api/me': { user: 'u', role },
    '/api/reports/metrics': { from: '', to: '', standard: z, expedited: z, drugStandard: z, drugExpedited: z, services: [],
      superseded: 0, notTracked: [] },
    '/api/reports/pends': { from: '', to: '', forInformation: {}, missing: [], contradicted: [] },
    '/api/reports/overrides': { from: '', to: '', by: 'clause', rows: [] },
    '/api/reports/list': { from: '', to: '', criteria: { minRequests: 30, minApprovedPercent: 95, minReversedPercent: 50 },
      note: '', rows: [] },
    '/api/goldcard': { enabled: true, standings: [], programs: [{ name: 'Texas', states: ['TX'], minApprovalRate: 0.9,
      lookBackDays: 365, minDecisions: 5, count: 'states' }] },
  })

  it('lets an administrator switch what a program counts', async () => {
    window.location.hash = '#/reports'
    respond(routes('admin'))
    renderApp()
    const states = (await screen.findByRole('radio', { name: 'Count only decisions for patients in TX' })) as HTMLInputElement
    expect(states.checked).toBe(true)
    expect(states.disabled).toBe(false)
  })

  it('shows a reviewer the setting without letting them change it', async () => {
    window.location.hash = '#/reports'
    respond(routes('reviewer'))
    renderApp()
    const all = (await screen.findByRole('radio', { name: /Count every decision/ })) as HTMLInputElement
    expect(all.disabled).toBe(true)
  })
})

describe('an ended session', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    window.location.hash = ''
  })

  it('goes back to sign-in when any request is refused', async () => {
    let signedIn = true
    vi.stubGlobal('fetch', async (input: Request | string) => {
      const path = new URL(typeof input === 'string' ? input : input.url, 'http://localhost').pathname
      if (path === '/api/me' && signedIn) {
        return new Response(JSON.stringify({ user: 'dr.lee', role: 'reviewer' }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      signedIn = false // the server restarted after the first answer
      return new Response(JSON.stringify({ error: 'the session has ended; sign in again' }), { status: 401 })
    })
    renderApp()
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeTruthy()
  })
})

describe('the policy editor', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    window.location.hash = ''
  })

  const draft = {
    id: 'knee', source: 'policy knee\n  title "Knee"\n', saved: '2026-10-13T10:00:00Z', ready: true, inForce: '0.1.0',
    policy: { id: 'knee', title: 'Knee', version: '0.2.0', status: 'draft' },
    check: { policy: 'knee', combinations: 4, outcomes: { 'approved/decided': 1, 'denied/decided': 3 }, reached: {},
      unreachable: null, defects: null, ok: true, warnings: ['status draft: nobody has signed this policy off'] },
  }

  it('lists drafts for an author, with what state each is in', async () => {
    window.location.hash = '#/drafts'
    respond({
      '/api/me': { user: 'ana', role: 'viewer', scopes: ['author'], editing: true },
      '/api/drafts': [draft, { ...draft, id: 'hip', ready: false, check: undefined, error: 'hip.vaglio:2:3: expected a string' }],
      '/api/policies': [],
    })
    renderApp()
    expect(await screen.findByRole('link', { name: 'knee' })).toBeTruthy()
    expect(screen.getByText('ready to sign')).toBeTruthy()
    expect(screen.getByText('does not parse')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Drafts' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Start a draft' })).toBeTruthy()
  })

  it('offers the sign-off to a clinician only, and says what it replaces', async () => {
    window.location.hash = '#/draft/knee'
    respond({ '/api/me': { user: 'dr.lee', role: 'reviewer', npi: '1234567893', credential: 'MD', editing: true },
      '/api/drafts/knee': draft })
    renderApp()
    expect(await screen.findByText(/Version 0.1.0 is in force/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Sign off and put in force…' })).toBeTruthy()
    // A reviewer is not an author: the source is read-only.
    expect((screen.getByLabelText('Source') as HTMLTextAreaElement).readOnly).toBe(true)
    cleanup()
    respond({ '/api/me': { user: 'ana', role: 'admin', editing: true }, '/api/drafts/knee': draft })
    renderApp()
    expect(await screen.findByText(/A clinician signs a policy off/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Sign off and put in force…' })).toBeNull()
  })

  it('hides the Drafts tab when the editor is off', async () => {
    respond({ '/api/me': { user: 'ana', role: 'admin', editing: false }, '/api/cases': [] })
    renderApp()
    expect(await screen.findByRole('link', { name: 'Cases' })).toBeTruthy()
    expect(screen.queryByRole('link', { name: 'Drafts' })).toBeNull()
  })
})

describe('scopes, sign-on and deadlines', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    window.location.hash = ''
  })

  it('shows the audit log to a viewer with the audit scope', async () => {
    respond({ '/api/me': { user: 'aud', role: 'viewer', scopes: ['audit'], editing: false }, '/api/cases': [] })
    renderApp()
    expect(await screen.findByRole('link', { name: 'Audit log' })).toBeTruthy()
  })

  it('offers single sign-on, and says when it found no account', async () => {
    window.location.hash = '#/signin?sso=unknown'
    respond({ '/api/me': 401, '/api/sso': { enabled: true, label: 'Sign in with Example Health' } })
    renderApp()
    const link = (await screen.findByRole('link', { name: 'Sign in with Example Health' })) as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('/api/sso/start')
    expect(screen.getByText(/there is no account here for you/)).toBeTruthy()
  })

  it('offers an extension on a waiting case before it is due', async () => {
    window.location.hash = '#/case/c1'
    const due = new Date(Date.now() + 3 * 86400e3).toISOString()
    respond({
      '/api/me': { user: 'r', role: 'reviewer', editing: false },
      '/api/cases/c1': { ...pendedCase, due, maxExtensionDays: 14 },
      '/api/cases/c1/documents': { reader: false, fetch: false, documents: [], skipped: [] },
    })
    renderApp()
    expect(await screen.findByText('Extend the decision timeframe')).toBeTruthy()
    expect(screen.getAllByText(/in 3 days/).length).toBeGreaterThan(0)
  })

  it('shows an overdue case in the queue', async () => {
    window.location.hash = '#/cases'
    respond({
      '/api/me': { user: 'r', role: 'reviewer', editing: false },
      '/api/cases': [{ id: 'c1', created: '2026-10-01T10:00:00Z', updated: '', status: 'pended', policy: 'p',
        policyTitle: 'P', item: '1', service: 'x|1', claim: 'c|1', patient: 'Patient/p', decision: 'pend',
        reason: 'needs_information', due: new Date(Date.now() - 5 * 36e5).toISOString(), extended: true }],
    })
    renderApp()
    expect(await screen.findByText(/overdue by 5 h/)).toBeTruthy()
    expect(screen.getByText(/· extended/)).toBeTruthy()
  })
})
