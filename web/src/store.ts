import { configureStore, createSlice, type PayloadAction } from '@reduxjs/toolkit'
import { createApi, fetchBaseQuery, setupListeners } from '@reduxjs/toolkit/query/react'
import type {
  AuditPage,
  CaseDetail,
  CaseStatus,
  CaseSummary,
  CheckReport,
  DecideRequest,
  Draft,
  Extension,
  SourceCheck,
  SSOInfo,
  DecideResult,
  DocumentList,
  Finding,
  GoldCard,
  Me,
  Metrics,
  Overrides,
  Pends,
  Reading,
  PolicySummary,
  ReplayReport,
  ReceiptCheck,
  DraftingInfo,
  DraftJob,
  PolicyVersion,
  ReplayedCase,
  Letter,
  PolicyDetail,
  DecisionMap,
  ReviewRequest,
  Verification,
} from './types'

// Every request that changes anything is JSON from this origin, which is what the server's cross-site check requires.
const raw = fetchBaseQuery({ baseUrl: '/api', credentials: 'same-origin' })

// A 401 on anything means the session has ended - idle, a restart, an account changed - so whatever the console holds is
// stale: drop it all, which asks who is signed in again and shows the sign-in form.
const baseQuery: typeof raw = async (args, apiCtx, extra) => {
  const result = await raw(args, apiCtx, extra)
  const url = typeof args === 'string' ? args : args.url
  if (result.error?.status === 401 && url !== 'me' && url !== 'login') {
    apiCtx.dispatch(api.util.resetApiState())
  }
  return result
}

export const api = createApi({
  reducerPath: 'api',
  baseQuery,
  tagTypes: ['Me', 'Case', 'Audit', 'Draft', 'Policy'],
  endpoints: (b) => ({
    me: b.query<Me, void>({ query: () => 'me', providesTags: ['Me'] }),
    login: b.mutation<Me, { name: string; password: string }>({
      query: (body) => ({ url: 'login', method: 'POST', body }),
      invalidatesTags: ['Me', 'Case', 'Audit'],
    }),
    logout: b.mutation<void, void>({
      query: () => ({ url: 'logout', method: 'POST', body: {} }),
      invalidatesTags: ['Me'],
    }),
    sso: b.query<SSOInfo, void>({ query: () => 'sso' }),
    demo: b.query<{ name: string; password: string }, void>({ query: () => 'demo' }),
    policyDetail: b.query<PolicyDetail, string>({
      query: (id) => `policies/${encodeURIComponent(id)}`,
      providesTags: ['Policy'],
    }),
    policyMap: b.query<DecisionMap, string>({
      query: (id) => `policies/${encodeURIComponent(id)}/map`,
      providesTags: ['Policy'],
    }),
    letter: b.query<Letter, { id: string; lang: string }>({
      query: ({ id, lang }) => `cases/${encodeURIComponent(id)}/letter?lang=${lang}`,
      providesTags: ['Case'],
    }),
    policyVersions: b.query<PolicyVersion[], string>({
      query: (id) => `policies/${encodeURIComponent(id)}/versions`,
      providesTags: ['Policy'],
    }),
    policyVersionSource: b.query<string, { id: string; sha: string }>({
      query: ({ id, sha }) => ({
        url: `policies/${encodeURIComponent(id)}/versions/${sha}`,
        responseHandler: 'text',
      }),
    }),
    replayCase: b.mutation<ReplayedCase, { source: string; case: string }>({
      query: (body) => ({ url: 'replay/case', method: 'POST', body }),
    }),
    drafting: b.query<DraftingInfo, void>({ query: () => 'drafting' }),
    startDrafting: b.mutation<DraftJob, { id: string; name: string; contentType: string; data: string }>({
      query: (body) => ({ url: 'drafting', method: 'POST', body }),
    }),
    draftJob: b.query<DraftJob, string>({ query: (job) => `drafting/${job}`, providesTags: ['Draft'] }),
    receipt: b.query<ReceiptCheck, string>({ query: (code) => `receipts/${encodeURIComponent(code)}` }),
    policies: b.query<PolicySummary[], void>({ query: () => 'policies', providesTags: ['Policy'] }),
    policySource: b.query<string, string>({
      query: (id) => ({ url: `policies/${encodeURIComponent(id)}/source`, responseHandler: 'text' }),
      providesTags: ['Policy'],
    }),
    policyCheck: b.query<CheckReport, string>({
      query: (id) => `policies/${encodeURIComponent(id)}/check`,
      providesTags: ['Policy'],
    }),
    checkSource: b.mutation<SourceCheck, string>({
      query: (source) => ({ url: 'policies/check', method: 'POST', body: { source } }),
    }),
    drafts: b.query<Draft[], void>({ query: () => 'drafts', providesTags: ['Draft'] }),
    draft: b.query<Draft, string>({
      query: (id) => `drafts/${encodeURIComponent(id)}`,
      providesTags: (_r, _e, id) => [{ type: 'Draft', id }],
    }),
    saveDraft: b.mutation<Draft, { id: string; source: string }>({
      query: ({ id, source }) => ({ url: `drafts/${encodeURIComponent(id)}`, method: 'PUT', body: { source } }),
      invalidatesTags: ['Draft', 'Audit'],
    }),
    removeDraft: b.mutation<void, string>({
      query: (id) => ({ url: `drafts/${encodeURIComponent(id)}`, method: 'DELETE' }),
      invalidatesTags: ['Draft', 'Audit'],
    }),
    draftFrom: b.mutation<Draft, string>({
      query: (id) => ({ url: `policies/${encodeURIComponent(id)}/draft`, method: 'POST', body: {} }),
      invalidatesTags: ['Draft', 'Audit'],
    }),
    signOff: b.mutation<unknown, string>({
      query: (id) => ({ url: `drafts/${encodeURIComponent(id)}/signoff`, method: 'POST', body: {} }),
      invalidatesTags: ['Draft', 'Policy', 'Audit'],
    }),
    extend: b.mutation<CaseDetail, { id: string; days: number; reason: Extension['reason']; note: string }>({
      query: ({ id, ...body }) => ({ url: `cases/${encodeURIComponent(id)}/extend`, method: 'POST', body }),
      invalidatesTags: ['Case', 'Audit'],
    }),
    decide: b.mutation<DecideResult, DecideRequest>({
      query: (body) => ({ url: 'decide', method: 'POST', body }),
    }),
    cases: b.query<CaseSummary[], CaseStatus | ''>({
      query: (status) => (status ? `cases?status=${status}` : 'cases'),
      providesTags: ['Case'],
    }),
    case: b.query<CaseDetail, string>({
      query: (id) => `cases/${encodeURIComponent(id)}`,
      providesTags: (_r, _e, id) => [{ type: 'Case', id }],
    }),
    review: b.mutation<CaseDetail, ReviewRequest>({
      query: ({ id, ...body }) => ({ url: `cases/${encodeURIComponent(id)}/review`, method: 'POST', body }),
      invalidatesTags: ['Case', 'Audit'],
    }),
    audit: b.query<AuditPage, number>({
      query: (after) => `audit?after=${after}&limit=100`,
      providesTags: ['Audit'],
    }),
    verify: b.query<Verification, void>({ query: () => 'audit/verify', providesTags: ['Audit'] }),
    metrics: b.query<Metrics, Period>({ query: (p) => `reports/metrics?${period(p)}`, providesTags: ['Case'] }),
    pends: b.query<Pends, Period>({ query: (p) => `reports/pends?${period(p)}`, providesTags: ['Case'] }),
    goldCard: b.query<GoldCard, void>({ query: () => 'goldcard', providesTags: ['Case'] }),
    goldCardCounting: b.mutation<unknown, { program: string; count: 'all' | 'states' }>({
      query: ({ program, count }) => ({
        url: `goldcard/programs/${encodeURIComponent(program)}/count`,
        method: 'PUT',
        body: { count },
      }),
      invalidatesTags: ['Case', 'Audit'],
    }),
    overrides: b.query<Overrides, Period & { by: string }>({
      query: ({ by, ...p }) => `reports/overrides?${period(p)}&by=${by}`,
      providesTags: ['Case'],
    }),
    documents: b.query<DocumentList, string>({
      query: (id) => `cases/${encodeURIComponent(id)}/documents`,
      providesTags: (_r, _e, id) => [{ type: 'Case', id }],
    }),
    read: b.mutation<Reading, { id: string; document: string }>({
      query: ({ id, document }) => ({ url: `cases/${encodeURIComponent(id)}/read`, method: 'POST', body: { document } }),
      invalidatesTags: (_r, _e, { id }) => [{ type: 'Case', id }, 'Audit'],
    }),
    confirm: b.mutation<CaseDetail, { id: string; reading: number; index: number; confirmed: boolean }>({
      query: ({ id, ...body }) => ({ url: `cases/${encodeURIComponent(id)}/confirm`, method: 'POST', body }),
      invalidatesTags: (_r, _e, { id }) => [{ type: 'Case', id }, 'Audit'],
    }),
    fetchDocuments: b.mutation<{ received: number }, string>({
      query: (id) => ({ url: `cases/${encodeURIComponent(id)}/fetch`, method: 'POST', body: {} }),
      invalidatesTags: (_r, _e, id) => [{ type: 'Case', id }, 'Audit'],
    }),
    appeal: b.mutation<CaseDetail, { id: string; outcome: 'upheld' | 'overturned'; reason: string; decided: string }>({
      query: ({ id, ...body }) => ({ url: `cases/${encodeURIComponent(id)}/appeal`, method: 'POST', body }),
      invalidatesTags: ['Case', 'Audit'],
    }),
    replay: b.mutation<ReplayReport, string>({
      query: (source) => ({ url: 'replay', method: 'POST', body: { source } }),
      invalidatesTags: ['Audit'],
    }),
  }),
})

/** A span of days, YYYY-MM-DD; empty is the server's default (this year to date). */
export interface Period {
  from: string
  to: string
}

function period(p: Period): string {
  return new URLSearchParams(Object.entries(p).filter(([, v]) => v)).toString()
}

export const {
  useMeQuery,
  useReceiptQuery,
  useDraftingQuery,
  useStartDraftingMutation,
  useDraftJobQuery,
  usePolicyVersionsQuery,
  usePolicyVersionSourceQuery,
  useReplayCaseMutation,
  useLetterQuery,
  usePolicyDetailQuery,
  usePolicyMapQuery,
  useLazyReceiptQuery,
  useSsoQuery,
  useDemoQuery,
  useCheckSourceMutation,
  useDraftsQuery,
  useDraftQuery,
  useSaveDraftMutation,
  useRemoveDraftMutation,
  useDraftFromMutation,
  useSignOffMutation,
  useExtendMutation,
  useLoginMutation,
  useLogoutMutation,
  usePoliciesQuery,
  usePolicySourceQuery,
  usePolicyCheckQuery,
  useDecideMutation,
  useCasesQuery,
  useCaseQuery,
  useReviewMutation,
  useAuditQuery,
  useVerifyQuery,
  useMetricsQuery,
  usePendsQuery,
  useGoldCardQuery,
  useGoldCardCountingMutation,
  useReplayMutation,
  useOverridesQuery,
  useDocumentsQuery,
  useReadMutation,
  useConfirmMutation,
  useFetchDocumentsMutation,
  useAppealMutation,
} = api

/** What the person trying a policy has entered for one piece of evidence. Unaddressed is the absence of an entry. */
export interface EvidenceEntry {
  finding: Finding | 'unaddressed'
  reference: string
  quote: string
}

export interface RequestState {
  policy: string | null
  evidence: Record<string, EvidenceEntry>
}

const initialState: RequestState = { policy: null, evidence: {} }

export const blank: EvidenceEntry = { finding: 'unaddressed', reference: '', quote: '' }

export const requestSlice = createSlice({
  name: 'request',
  initialState,
  reducers: {
    selectPolicy(state, action: PayloadAction<string>) {
      state.policy = action.payload
      state.evidence = {}
    },
    setEvidence(state, action: PayloadAction<{ argument: string; patch: Partial<EvidenceEntry> }>) {
      const { argument, patch } = action.payload
      state.evidence[argument] = { ...(state.evidence[argument] ?? blank), ...patch }
    },
  },
})

export const { selectPolicy, setEvidence } = requestSlice.actions

/** Builds the API request: only addressed evidence is sent. */
export function toRequest(state: RequestState): DecideRequest | null {
  if (!state.policy) return null
  const evidence = Object.entries(state.evidence)
    .filter(([, e]) => e.finding !== 'unaddressed')
    .map(([argument, e]) => ({
      argument,
      finding: e.finding as Finding,
      reference: e.reference.trim(),
      quote: e.quote.trim(),
    }))
  return { policy: state.policy, evidence }
}

/** Where the console is: one screen, and what it shows. Kept in the URL's hash so a link and a reload land in place. */
export type Screen = 'cases' | 'case' | 'try' | 'policies' | 'policy' | 'reports' | 'audit' | 'drafts' | 'draft'

export interface ViewState {
  screen: Screen
  id: string | null
}

export function parseHash(hash: string): ViewState {
  const [screen, id] = hash.replace(/^#\/?/, '').split('/')
  switch (screen) {
    case 'case':
    case 'policy':
    case 'draft': {
      const list = { case: 'cases', policy: 'policies', draft: 'drafts' } as const
      return id ? { screen, id: decodeURIComponent(id) } : { screen: list[screen], id: null }
    }
    case 'drafts':
    case 'try':
    case 'policies':
    case 'reports':
    case 'audit':
    case 'cases':
      return { screen, id: null }
  }
  return { screen: 'cases', id: null }
}

export function toHash(v: ViewState): string {
  return '#/' + v.screen + (v.id ? '/' + encodeURIComponent(v.id) : '')
}

export const viewSlice = createSlice({
  name: 'view',
  initialState: { screen: 'cases', id: null } as ViewState,
  reducers: {
    go(_state, action: PayloadAction<ViewState>) {
      return action.payload
    },
  },
})

export const { go } = viewSlice.actions

export function makeStore() {
  const store = configureStore({
    reducer: { [api.reducerPath]: api.reducer, request: requestSlice.reducer, view: viewSlice.reducer },
    middleware: (gdm) => gdm().concat(api.middleware),
  })
  setupListeners(store.dispatch) // refetchOnFocus
  return store
}

export type AppStore = ReturnType<typeof makeStore>
export type RootState = ReturnType<AppStore['getState']>
