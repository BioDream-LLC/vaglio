// Types mirror the Go structs' JSON tags (internal/decide, internal/policy, internal/cases, internal/server). Keep them in
// step.

export type Role = 'viewer' | 'reviewer' | 'admin'

export interface Me {
  user: string
  role: Role
  npi?: string
  credential?: string
  /** This deployment lets only clinicians deny. */
  clinicianDenials: boolean
  /** Jobs beside the role: author, audit, appeals, policy:ID. */
  scopes?: string[]
  /** How this session signed in: password or sso. */
  via?: string
  /** The policy editor is on. */
  editing: boolean
  /** A public demonstration on invented data. */
  demo?: boolean
}

/** Whether a session may do a job: the role, or the scope beside it. */
export function can(me: Me, role: Role, scope?: string): boolean {
  const rank: Record<Role, number> = { viewer: 1, reviewer: 2, admin: 3 }
  if (rank[me.role] >= rank[role]) return true
  return !!scope && (me.scopes ?? []).includes(scope)
}

export interface Argument {
  name: string
  kind: 'evidence' | 'criterion' | 'decision'
  text: string
  clause: string
  verdict?: 'approve' | 'deny'
}

export interface Service {
  system: string
  code: string
}

export interface PolicySummary {
  id: string
  title: string
  /** Empty for a new policy that is only scheduled: nothing of it is in force yet. */
  version: string
  source: string
  evidence: Argument[]
  services: Service[]
  authority?: 'ncd' | 'lcd' | 'state' | 'plan'
  effective?: string
  ends?: string
  lines?: string[]
  states?: string[]
  inEffect: boolean
  /** The version signed and waiting for its effective date. */
  next?: { version: string; effective: string; title: string }
}

/** A policy for the same service that did not decide this request, and why. */
export interface Aside {
  policy: string
  version: string
  title: string
  why: string
}

/** Why a case's policy decided it when others are for the same service. */
export interface Precedence {
  why: string
  setAside?: Aside[]
  unsettled?: Aside[]
}

export interface CheckReport {
  policy: string
  combinations: number
  outcomes: Record<string, number>
  reached: Record<string, number>
  unreachable: string[] | null
  defects: string[][] | null
  ok: boolean
  warnings: string[]
}

export type Finding = 'present' | 'absent'

export interface Assertion {
  argument: string
  finding: Finding
  reference: string
  quote: string
}

export interface DecideRequest {
  policy: string
  evidence: Assertion[]
  /** A kept version to decide with instead of the one in force. */
  version?: string
}

export interface Citation {
  reference: string
  quote: string
}

export interface ProofNode {
  argument: string
  kind: string
  text: string
  clause: string
  citation?: Citation
  defeats?: { attacker: string; text: string; by: ProofNode }[]
}

export interface Item {
  argument: string
  text: string
  clause: string
  citation?: Citation
}

export type Outcome = 'approved' | 'denied' | 'pended' | 'policy_defect'
export type Reason = 'decided' | 'needs_information' | 'indeterminate' | 'unsatisfied' | 'conflicted' | 'contradictory_record'

export interface DecideResult {
  policy: string
  policyVersion: string
  outcome: Outcome
  reason: Reason
  decision?: string
  proof?: ProofNode
  missing?: Item[]
  assumptions?: Item[]
  relied?: Item[]
  contradicted?: Item[]
  toApprove?: Change[][]
  labels: Record<string, string>
}

export interface Change extends Item {
  contradictsRecord?: boolean
}

export interface ExtractedFinding extends Citation {
  argument: string
  finding: Finding
}

export interface Extraction {
  findings: ExtractedFinding[]
  contradictions: { argument: string; present: ExtractedFinding[]; absent: ExtractedFinding[] }[]
  ignored: string[]
  unread: { reference: string; what: string }[]
}

export type CaseStatus = 'decided' | 'pended' | 'reviewed' | 'superseded'

export interface Delivery {
  time: string
  ok: boolean
  error?: string
  final?: boolean
}

export interface CaseSummary {
  id: string
  created: string
  updated: string
  status: CaseStatus
  policy: string
  policyTitle: string
  item: string
  service: string
  claim: string
  patient: string
  decision: 'approve' | 'deny' | 'pend'
  reason: Reason
  review?: 'approve' | 'deny'
  delivery?: Delivery
  goldCard?: boolean
  proposed?: 'deny'
  expedited?: boolean
  due: string
  extended?: boolean
}

export interface Review {
  by: string
  time: string
  decision: 'approve' | 'deny'
  reason: string
  reviewerNpi?: string
  credential?: string
}

export interface AuditRecord {
  seq: number
  time: string
  actor: string
  action: string
  case?: string
  data?: unknown
  prev: string
  hash: string
}

export interface CaseDetail {
  id: string
  created: string
  updated: string
  status: CaseStatus
  policy: string
  policyVersion: string
  policyTitle: string
  item: string
  service: string
  claim: string
  patient: string
  provider: string
  request?: string
  bundle: string
  answer: {
    decision: 'approve' | 'deny' | 'pend'
    why?: string
    requested?: string[]
    questionnaire?: string
    attachments?: string[]
  }
  result: DecideResult
  evidence: Extraction
  review?: Review
  delivery?: Delivery
  supersededBy?: string
  priority?: string
  providerNpi?: string
  exemption?: Exemption
  proposed?: { decision: 'deny'; why: string }
  precedence?: Precedence
  patientState?: string
  drug?: boolean
  appeal?: Appeal
  extension?: Extension
  due: string
  maxExtensionDays: number
  readings?: Reading[]
  confirmations?: Confirmation[]
  received?: { time: string; bundle: string; resources: string[] }[]
  redecided?: { policyVersion: string; confirmed: number; received: number; result: DecideResult; evidence: Extraction }
  redecideError?: string
  history: AuditRecord[]
  receipts: CaseReceipt[]
}

/** A decision on a case and the receipt anyone can check it with. */
export interface CaseReceipt {
  receipt: string
  action: string
  time: string
  seq: number
}

/** What checking a receipt tells anyone holding it: nothing about the patient. */
export interface ReceiptCheck {
  receipt: string
  decision: 'approve' | 'deny' | 'pend' | 'overturned' | 'upheld'
  by: 'engine' | 'reviewer' | 'clinician' | 'appeal'
  credential?: string
  time: string
  policy: string
  policyVersion: string
  policyTitle: string
  clause?: string
  goldCard?: boolean
  proposed?: boolean
  later?: { receipt: string; decision: ReceiptCheck['decision']; by: ReceiptCheck['by']; time: string }[]
  record: { seq: number; hash: string; prev: string }
  chain: { verified: boolean; records: number; head: string }
}

export interface Appeal {
  by: string
  time: string
  outcome: 'upheld' | 'overturned'
  reason: string
  decided?: string
}

export type ExtensionReason = 'requested' | 'evidence' | 'exceptional'

export interface Extension {
  by: string
  time: string
  days: number
  reason: ExtensionReason
  note: string
  due: string
}

export interface Proposal {
  argument: string
  text: string
  finding: Finding
  document: string
  quote: string
}

export interface Reading {
  document: string
  model: string
  proposals: Proposal[]
  refused: string[]
  truncated?: boolean
}

export interface Confirmation extends Proposal {
  reading: number
  index: number
  confirmed: boolean
  by: string
  time: string
}

export interface DocumentList {
  reader: boolean
  fetch: boolean
  documents: { reference: string; what: string; truncated?: boolean; contentType?: string; extractedBy?: string }[]
  skipped: string[]
}

export interface OverrideRow {
  key: string
  label: string
  policy?: string
  denials: number
  overridden: number
  appealed: number
  overturned: number
  reversalPercent: number
  cases: string[]
}

export interface Overrides {
  from: string
  to: string
  by: 'clause' | 'provider' | 'state'
  rows: OverrideRow[]
}

/** One service in the prior authorization list review. */
export interface ListRow {
  service: string
  code: string
  policy: string
  title: string
  requests: number
  approved: number
  approvedPercent: number
  engineApproved: number
  pended: number
  exempt: number
  denied: number
  engineDenials: number
  reversed: number
  reversedPercent: number | null
  waiting: number
  medianReviewHours: number | null
  verdict: 'consider-removing' | 'keep' | 'not-enough'
  why: string
}

export interface ListReview {
  from: string
  to: string
  criteria: { minRequests: number; minApprovedPercent: number; minReversedPercent: number }
  rows: ListRow[]
  note: string
}

export interface Exemption {
  program: string
  basis?: string
  npi: string
  policy: string
  lookBackDays: number
  decisions: number
  approved: number
  minRate: number
  minDecisions: number
  counted: string[]
}

export interface MetricsGroup {
  requests: number
  approved: number
  denied: number
  pending: number
  approvedPercent: number | null
  deniedPercent: number | null
  realTime: number
  afterReview: number
  goldCard: number
  appealed: number
  approvedAfterAppeal: number
  approvedAfterAppealPercent: number | null
  extended: number
  approvedAfterExtension: number
  approvedAfterExtensionPercent: number | null
  late: number
  averageHours: number | null
  medianHours: number | null
}

export interface Metrics {
  from: string
  to: string
  standard: MetricsGroup
  expedited: MetricsGroup
  drugStandard: MetricsGroup
  drugExpedited: MetricsGroup
  services: { policy: string; title: string; service: string; requests: number; approved: number; denied: number; pending: number }[]
  superseded: number
  notTracked: string[]
}

export interface Cause {
  policy: string
  argument: string
  text: string
  question?: string
  pends: number
  percent: number
}

export interface Pends {
  from: string
  to: string
  forInformation: Record<string, number>
  missing: Cause[]
  contradicted: Cause[]
}

export interface Program {
  name: string
  basis?: string
  states?: string[]
  policies?: string[]
  minApprovalRate: number
  lookBackDays: number
  minDecisions: number
  count?: 'all' | 'states'
}

export interface Standing {
  program: string
  npi: string
  policy: string
  decisions: number
  approved: number
  rate: number
  exempt: boolean
}

export interface GoldCard {
  enabled: boolean
  programs: Program[]
  standings: Standing[]
}

export interface ReplayRow {
  case: string
  created: string
  claim: string
  item: string
  version: string
  was: string
  final?: string
  now?: string
  reason?: string
  note?: string
  error?: string
}

export interface ReplayReport {
  policy: string
  version: string
  checkOk: boolean
  warnings: string[]
  cases: number
  unchanged: number
  changed: number
  errors: number
  transitions: Record<string, number>
  rows: ReplayRow[]
}

export interface ReviewRequest {
  id: string
  decision: 'approve' | 'deny'
  reason: string
  reviewerNpi: string
}

export interface AuditPage {
  records: AuditRecord[]
  total: number
}

export interface Verification {
  ok: boolean
  records: number
  head: string
  error?: string
}

/** A policy source as the editor checks it: where it fails to parse, or its check, and whether it could be signed. */
export interface SourceCheck {
  error?: string
  line?: number
  col?: number
  policy?: { id: string; title: string; version: string; status?: string; effective?: string }
  check?: CheckReport
  ready: boolean
  notReady?: string
  inForce?: string
}

export interface Draft extends SourceCheck {
  id: string
  source: string
  saved: string
}

export interface SSOInfo {
  enabled: boolean
  label: string
}

/** A policy as the engine reads it: every argument and who defeats whom. */
export interface PolicyDetail {
  id: string
  title: string
  version: string
  arguments: Argument[]
  attacks: { from: string; to: string }[]
}

/** Every combination of a policy's evidence and its outcome on a fully documented record. */
export interface DecisionMap {
  policy: string
  evidence: string[]
  /** One letter per combination: a approved, d denied, p pended, x policy defect. Bit i of the index is evidence i. */
  outcomes: string
  decisions: number[]
  clauses: string[]
}

/** A decision's letter, in plain words, as sections of text. */
export interface Letter {
  lang: 'en' | 'es'
  title: string
  date: string
  sections: { heading: string; paragraphs?: string[]; items?: string[]; quote?: string }[]
  quoted?: string
  receipt?: string
}

/** A version of a policy that has been in force, kept by the server. */
export interface PolicyVersion {
  policy: string
  version: string
  title: string
  sha256: string
  since: string
}

/** A case decided again by another version, beside the decision it was given. */
export interface ReplayedCase {
  was: DecideResult
  now: DecideResult
}

/** Drafting a policy from an uploaded coverage document. */
export interface DraftingInfo {
  enabled: boolean
  pdf: boolean
  model?: string
  /** The latest drafting on this server, and whether it is still running. */
  last?: string
  busy?: boolean
}

export type FindingKind = 'contradiction' | 'gap' | 'quote' | 'code' | 'syntax' | 'lint'

export interface DraftJob {
  id: string
  policy: string
  document: string
  sha256: string
  state: 'running' | 'passed' | 'failed'
  started: string
  rounds: { findings: { kind: FindingKind; text: string }[] }[]
  error?: string
  saved: boolean
  check?: { combinations: number; outcomes: Record<string, number> }
}
