import { describe, expect, test } from 'bun:test'
import { parseShellMessage } from '../app/utils/stacks'

describe('Shell events', () => {
  const operation = {
    id: 'shell-1',
    stack: 'app',
    action: 'shell',
    status: 'running',
    output: '$ ',
    exit_code: null,
  }

  test('snapshot', () => {
    expect(
      parseShellMessage(
        JSON.stringify({
          type: 'shell.event',
          stream: 'reader-1',
          shell: 'shell-1',
          event: 'operation.snapshot',
          payload: operation,
        }),
      ),
    ).toEqual({
      type: 'shell.event',
      stream: 'reader-1',
      shell: 'shell-1',
      event: { event: 'operation.snapshot', operation },
    })
  })

  test('output', () => {
    expect(
      parseShellMessage(
        JSON.stringify({
          type: 'shell.event',
          stream: 'reader-1',
          shell: 'shell-1',
          event: 'operation.output',
          payload: { id: 'shell-1', output: 'héllo 🐚' },
        }),
      ),
    ).toEqual({
      type: 'shell.event',
      stream: 'reader-1',
      shell: 'shell-1',
      event: { event: 'operation.output', id: 'shell-1', output: 'héllo 🐚' },
    })
  })

  test('done', () => {
    const done = { ...operation, status: 'succeeded', exit_code: 0 }
    expect(
      parseShellMessage(
        JSON.stringify({
          type: 'shell.event',
          stream: 'reader-1',
          shell: 'shell-1',
          event: 'operation.done',
          payload: done,
        }),
      ),
    ).toEqual({
      type: 'shell.event',
      stream: 'reader-1',
      shell: 'shell-1',
      event: { event: 'operation.done', operation: done },
    })
  })

  test('error', () => {
    const message = {
      type: 'shell.error',
      stream: 'reader-1',
      shell: 'shell-1',
      error: 'Node unavailable',
    }
    expect(parseShellMessage(JSON.stringify(message))).toEqual(message)
  })

  test('malformed', () => {
    for (const value of [
      null,
      [],
      { type: 'shell.error', stream: 'reader-1', shell: 'shell-1', error: {} },
      { type: 'shell.event', event: 'operation.snapshot', payload: operation },
      { type: 'shell.event', stream: 'reader-1', shell: 'shell-1', event: 'unexpected', payload: operation },
      {
        type: 'shell.event',
        stream: 'reader-1',
        shell: 'shell-1',
        event: 'operation.done',
        payload: { id: 'shell-1' },
      },
      {
        type: 'shell.event',
        stream: 'reader-1',
        shell: 'shell-1',
        event: 'operation.output',
        payload: { id: 'shell-1', output: 2 },
      },
    ]) {
      expect(() => parseShellMessage(JSON.stringify(value))).toThrow()
    }
    expect(() => parseShellMessage('invalid JSON')).toThrow()
  })
})
