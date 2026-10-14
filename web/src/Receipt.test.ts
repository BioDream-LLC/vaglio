import { describe, expect, it } from 'vitest'
import { receiptFromHash, receiptURL } from './Receipt'

describe('receipts', () => {
  it('reads the receipt page from the hash', () => {
    expect(receiptFromHash('#/receipt/VG-0123-4567-89AB-CDEF')).toBe('VG-0123-4567-89AB-CDEF')
    expect(receiptFromHash('#/receipt')).toBe('')
    expect(receiptFromHash('#/receipt/')).toBe('')
    expect(receiptFromHash('#/cases')).toBeNull()
    expect(receiptFromHash('')).toBeNull()
  })
  it('links to this server', () => {
    expect(receiptURL('VG-1', 'https://vaglio.example')).toBe('https://vaglio.example/#/receipt/VG-1')
  })
})
