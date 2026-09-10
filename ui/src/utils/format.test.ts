import { describe, expect, it } from 'vitest'

import { formatBytes } from './format'

describe('formatBytes', () => {
  it('formats byte counts by magnitude', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(2048)).toBe('2.0 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(formatBytes(3 * 1024 * 1024 * 1024)).toBe('3.0 GB')
  })

  it('renders a placeholder for a value the client never emitted', () => {
    expect(formatBytes(undefined)).toBe('-')
    expect(formatBytes(NaN)).toBe('-')
  })
})
