import { describe, expect, it } from 'vitest'
import { layers } from './PolicyMap'

describe('policy map', () => {
  it('lays evidence first, decisions last, and each criterion right of what defeats it', () => {
    const l = layers({
      arguments: [
        { name: 'e', kind: 'evidence', text: '', clause: '' },
        { name: 'c1', kind: 'criterion', text: '', clause: '' },
        { name: 'c2', kind: 'criterion', text: '', clause: '' },
        { name: 'ok', kind: 'decision', text: '', clause: '', verdict: 'approve' },
      ],
      attacks: [
        { from: 'e', to: 'c1' },
        { from: 'c1', to: 'c2' },
        { from: 'c2', to: 'ok' },
      ],
    })
    expect([l.get('e'), l.get('c1'), l.get('c2'), l.get('ok')]).toEqual([0, 1, 2, 3])
  })
})
