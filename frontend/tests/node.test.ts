import { describe, expect, test } from 'bun:test'
import { formatAgentUptime, formatMemoryBytes } from '../app/utils/node'

describe('Node details', () => {
  test('memory capacity', () => {
    expect(formatMemoryBytes(0)).toBe('0 B')
    expect(formatMemoryBytes(1024)).toBe('1 KiB')
    expect(formatMemoryBytes(16 * 1024 ** 3)).toBe('16 GiB')
    expect(formatMemoryBytes(1.25 * 1024 ** 4)).toBe('1.3 TiB')
  })

  test('agent uptime', () => {
    const started = '2026-10-25T09:00:00Z'
    expect(formatAgentUptime(started, Date.parse(started))).toBe('0h 0m')
    expect(formatAgentUptime(started, Date.parse('2026-10-26T11:03:00Z'))).toBe('1d 2h 3m')
    const unavailable = formatAgentUptime('invalid')
    expect(unavailable).not.toBe('')
    expect(unavailable).not.toContain('NaN')
    expect(formatAgentUptime(started, Date.parse('2026-10-25T08:00:00Z'))).toBe(unavailable)
  })
})
