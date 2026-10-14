import { describe, expect, it } from 'vitest'
import { baseline, changed, sandboxRequest, SUPPOSED } from './WhatIf'
import type { CaseDetail } from './types'

const c = {
  evidence: {
    findings: [
      { argument: 'red_flag', finding: 'absent', reference: 'QR/1#a', quote: 'No' },
      { argument: 'red_flag', finding: 'absent', reference: 'QR/1#b', quote: 'No again' },
      { argument: 'therapy', finding: 'present', reference: 'QR/1#c', quote: 'Yes' },
      { argument: 'both', finding: 'present', reference: 'QR/1#d', quote: 'Yes' },
    ],
    contradictions: [{ argument: 'both', present: [], absent: [] }],
    ignored: [],
    unread: [],
  },
} as unknown as CaseDetail

describe('what-if sandbox', () => {
  it('starts from the record: the first citation per evidence, contradicted evidence unaddressed', () => {
    const b = baseline(c)
    expect(Object.keys(b).sort()).toEqual(['red_flag', 'therapy'])
    expect(b.red_flag.reference).toBe('QR/1#a')
  })

  it('keeps the record citation where unchanged and marks a supposed finding as supposed', () => {
    const b = baseline(c)
    const req = sandboxRequest('p', b, { red_flag: 'present', therapy: 'present', both: 'unaddressed' })
    expect(req.evidence).toEqual([
      { argument: 'red_flag', finding: 'present', ...SUPPOSED },
      b.therapy,
    ])
  })

  it('reports exactly what differs from the record', () => {
    const b = baseline(c)
    expect([...changed(b, { red_flag: 'absent', therapy: 'present', both: 'unaddressed' })]).toEqual([])
    expect([...changed(b, { red_flag: 'unaddressed', therapy: 'present', both: 'present' })].sort()).toEqual([
      'both',
      'red_flag',
    ])
  })
})
