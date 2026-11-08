import { describe, expect, test } from 'bun:test'
import { parseStreamMessage } from '../app/utils/stacks'

describe('Stream messages', () => {
  test('event', () => {
    expect(
      parseStreamMessage(
        JSON.stringify({
          type: 'event',
          event: 'nodes.changed',
          payload: {},
        }),
      ),
    ).toEqual({ event: 'nodes.changed', data: '{}' })
  })

  test('log', () => {
    const payload = { message: 'héllo 🐚', event: 'log' }
    expect(
      parseStreamMessage(
        JSON.stringify({
          type: 'event',
          event: 'log',
          payload,
        }),
      ),
    ).toEqual({ event: 'log', data: JSON.stringify(payload) })
  })

  test('rejected', () => {
    const message = { type: 'error', status: 404, error: 'Operation not found' }
    expect(parseStreamMessage(JSON.stringify(message))).toEqual(message)
  })

  test('unavailable', () => {
    expect(parseStreamMessage('{"type":"error","error":"Node unavailable"}')).toEqual({
      type: 'error',
      error: 'Node unavailable',
    })
  })

  test('malformed', () => {
    for (const value of [
      null,
      [],
      { type: 'event', event: 'nodes.changed', payload: null },
      { type: 'event', event: 2, payload: {} },
      { type: 'event', event: 'nodes.changed', payload: [] },
      { type: 'error', error: {} },
      { type: 'error', status: '404', error: 'Missing' },
    ]) {
      expect(() => parseStreamMessage(JSON.stringify(value))).toThrow()
    }
    expect(() => parseStreamMessage('invalid JSON')).toThrow()
  })
})
