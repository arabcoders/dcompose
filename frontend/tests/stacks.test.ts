import { describe, expect, test } from 'bun:test'
import {
  compareStacks,
  containerProblem,
  containerCreatedAt,
  containerUptime,
  workspaceErrorMessage,
  imageUpdateLabel,
  imageUpdateTooltip,
  parseWorkspaceEvent,
} from '../app/utils/stacks'
import type { Service, StackStatus } from '../app/types'

test('stack order', () => {
  const stacks: Array<{ name: string; status: StackStatus }> = [
    { name: 'alpha', status: 'draft' },
    { name: 'zeta', status: 'running' },
    { name: 'alpha', status: 'unknown' },
    { name: 'alpha', status: 'stopped' },
    { name: 'beta', status: 'running' },
    { name: 'alpha', status: 'partial' },
    { name: 'alpha', status: 'running' },
  ]

  expect(stacks.sort(compareStacks).map((stack) => `${stack.status}/${stack.name}`)).toEqual([
    'running/alpha',
    'running/beta',
    'running/zeta',
    'partial/alpha',
    'stopped/alpha',
    'draft/alpha',
    'unknown/alpha',
  ])
})

describe('Container problems', () => {
  const service: Service = {
    name: 'app-web-1',
    service: 'web',
    image: 'nginx:alpine',
    state: 'running',
    health: '',
    ports: '',
  }

  test.each([
    { state: 'running', health: 'unhealthy' },
    { state: 'restarting' },
    { state: 'dead' },
    { state: 'exited', exit_code: 23 },
    { state: 'stopped', exit_code: 137 },
  ])('problem %j', (changes) => {
    expect(containerProblem({ ...service, ...changes })).not.toBeNull()
  })

  test.each([
    { state: 'running', health: 'healthy' },
    { state: 'running', health: 'starting' },
    { state: 'running', exit_code: 23 },
    { state: 'exited', exit_code: 0 },
    { state: 'exited' },
    { state: 'stopped', exit_code: 0, health: 'unhealthy' },
    { state: 'created' },
    { state: 'paused' },
  ])('normal %j', (changes) => {
    expect(containerProblem({ ...service, ...changes })).toBeNull()
  })

  test('exit detail', () => {
    expect(containerProblem({ ...service, state: 'exited', exit_code: 23 })).toContain('23')
  })
})

describe('Container timing', () => {
  const service: Service = {
    name: 'dagent', service: 'dagent', image: 'dagent', state: 'running', health: '', ports: '',
    created_at: '2026-11-02T00:39:58.237164057Z',
    started_at: '2026-11-03T00:39:58.706987971Z',
    status: 'stale-status-sentinel', running_for: 'stale-age-sentinel',
  }

  test('live uptime', () => {
    const started = new Date(service.started_at!).getTime()
    const before = containerUptime(service, new Date(started + 38000))
    const after = containerUptime(service, new Date(started + 39000))
    expect(before).toContain('38')
    expect(after).toContain('39')
    expect(after).not.toBe(before)
    expect(containerUptime(service, new Date('2026-11-03T03:55:52Z'))).toContain('3')
    expect(service.status).toBe('stale-status-sentinel')
  })

  test('restart', () => {
    const now = new Date('2026-11-03T03:55:52Z')
    const restarted = { ...service, started_at: '2026-11-03T03:55:14Z' }
    expect(containerUptime(restarted, now)).toContain('38')
  })

  test('time zones', () => {
    const now = new Date('2026-11-03T03:55:52Z')
    expect(containerUptime({ ...service, started_at: '2026-11-03T03:39:58.706987971+03:00' }, now))
      .toBe(containerUptime(service, now))
    expect(containerCreatedAt('2026-11-03T03:39:58+03:00', now))
      .toBe(containerCreatedAt('2026-11-03T00:39:58Z', now))
  })

  test('live creation age', () => {
    const created = '2026-11-03T00:39:58Z'
    expect(containerCreatedAt(created, new Date('2026-11-03T00:40:36Z'))).toContain('38')
    expect(containerCreatedAt(created, new Date('2026-11-03T00:40:37Z'))).toContain('39')
  })

  test.each([
    { state: 'exited' },
    { state: 'restarting' },
    { started_at: undefined },
    { started_at: 'invalid' },
  ])('no uptime %j', (changes) => {
    expect(containerUptime({ ...service, ...changes }, new Date())).toBeNull()
  })
})

describe('Workspace events', () => {
  test('snapshot', () => {
    const snapshot = {
      node: 'local',
      health: {
        status: 'ok',
        version: '2.3.4',
        api_version: 1,
        os: 'linux',
        arch: 'amd64',
        started_at: '2026-10-25T09:00:00Z',
      },
      stacks: [{ name: 'app', status: 'draft', services: [], loading: false }],
    }
    expect(parseWorkspaceEvent('node.snapshot', JSON.stringify(snapshot))).toEqual({
      event: 'node.snapshot',
      ...snapshot,
    })
  })

  test('runtime', () => {
    const runtime = {
      node: 'local',
      name: 'app',
      status: 'running',
      services: [
        {
          name: 'app-web-1',
          service: 'web',
          image: 'nginx:alpine',
          state: 'running',
          health: '',
          status: 'Up 2 hours',
          created_at: '2026-10-25 09:00:00 +0000 UTC',
          running_for: '2 days ago',
          ports: '8080:80',
          port_bindings: [
            { host_ip: '0.0.0.0', host_port: 8080, container_port: 80, protocol: 'tcp' },
          ],
          image_update: { status: 'available' },
          check_interval: '2h',
          auto_update: true,
        },
      ],
    }
    expect(parseWorkspaceEvent('stack.runtime', JSON.stringify(runtime))).toEqual({
      event: 'stack.runtime',
      node: 'local',
      runtime,
    })
  })

  test('malformed', () => {
    expect(() => parseWorkspaceEvent('stack.runtime', '{"node":"local"}')).toThrow()
    expect(() => parseWorkspaceEvent('stack.action', '{"node":"local","name":"app"}')).toThrow()
  })

  test('container timing', () => {
    const service = {
      name: 'app-web-1',
      service: 'web',
      image: 'nginx:alpine',
      state: 'exited',
      health: '',
      ports: '',
    }
    const runtime = { node: 'local', name: 'app', status: 'stopped', services: [service] }
    expect(parseWorkspaceEvent('stack.runtime', JSON.stringify(runtime))).not.toBeNull()
    for (const key of ['status', 'created_at', 'started_at', 'running_for', 'check_interval']) {
      const valid = { ...runtime, services: [{ ...service, [key]: 'timing' }] }
      expect(parseWorkspaceEvent('stack.runtime', JSON.stringify(valid))).not.toBeNull()
      const invalid = { ...runtime, services: [{ ...service, [key]: 1 }] }
      expect(() => parseWorkspaceEvent('stack.runtime', JSON.stringify(invalid))).toThrow()
    }
    for (const autoUpdate of [true, false]) {
      expect(
        parseWorkspaceEvent(
          'stack.runtime',
          JSON.stringify({
            ...runtime,
            services: [{ ...service, auto_update: autoUpdate }],
          }),
        ),
      ).not.toBeNull()
    }
    expect(() =>
      parseWorkspaceEvent(
        'stack.runtime',
        JSON.stringify({
          ...runtime,
          services: [{ ...service, auto_update: 'true' }],
        }),
      ),
    ).toThrow()
  })

  test('container exit', () => {
    const service = {
      name: 'app-web-1',
      service: 'web',
      image: 'nginx:alpine',
      state: 'exited',
      health: '',
      ports: '',
    }
    const runtime = { node: 'local', name: 'app', status: 'stopped', services: [service] }
    for (const exitCode of [0, 23]) {
      const event = parseWorkspaceEvent(
        'stack.runtime',
        JSON.stringify({
          ...runtime,
          services: [{ ...service, exit_code: exitCode }],
        }),
      )
      if ('stack.runtime' !== event?.event) {
        throw new Error('Missing runtime event')
      }
      expect(event.runtime.services[0]?.exit_code).toBe(exitCode)
    }
    for (const exitCode of ['23', null, 1.5]) {
      expect(() =>
        parseWorkspaceEvent(
          'stack.runtime',
          JSON.stringify({
            ...runtime,
            services: [{ ...service, exit_code: exitCode }],
          }),
        ),
      ).toThrow()
    }
  })

  test('health shape', () => {
    const snapshot = { node: 'local', stacks: [], health: { status: 'ok', version: '1.0.0' } }
    expect(() => parseWorkspaceEvent('node.snapshot', JSON.stringify(snapshot))).toThrow()
    expect(() =>
      parseWorkspaceEvent(
        'node.snapshot',
        JSON.stringify({
          ...snapshot,
          health: { ...snapshot.health, api_version: '1' },
        }),
      ),
    ).toThrow()
    expect(() =>
      parseWorkspaceEvent(
        'node.snapshot',
        JSON.stringify({
          ...snapshot,
          health: { ...snapshot.health, api_version: 1, os: 1 },
        }),
      ),
    ).toThrow()
  })

  test('health version', () => {
    const snapshot = {
      node: 'local',
      stacks: [],
      health: { status: 'ok', version: '2.0.0', api_version: 2 },
    }
    expect(parseWorkspaceEvent('node.snapshot', JSON.stringify(snapshot))).toEqual({
      event: 'node.snapshot',
      ...snapshot,
    })
  })

  test('unavailable message', () => {
    const state = {
      node: 'local',
      state: 'unavailable',
      message: 'Unsupported agent API version 2',
    }
    expect(parseWorkspaceEvent('node.state', JSON.stringify(state))).toEqual({
      event: 'node.state',
      ...state,
    })
  })

  test('live action', () => {
    const action = {
      node: 'node',
      name: 'app',
      action: 'deploy',
      phase: 'exit',
      exit_code: 0,
    }
    expect(parseWorkspaceEvent('stack.action', JSON.stringify(action))).toEqual({
      event: 'stack.action',
      ...action,
      message: undefined,
    })
    expect(parseWorkspaceEvent('stack.output', '{"node":"node","name":"app","output":"hello"}')).toEqual({
      event: 'stack.output',
      node: 'node',
      name: 'app',
      output: 'hello',
    })
  })

  test('tooltip fallback', () => {
    const update = { status: 'error' as const, reason: 'private-diagnostic-sentinel' }
    expect(imageUpdateTooltip(update)).toBe(imageUpdateLabel(update))
    expect(imageUpdateTooltip(update)).not.toContain(update.reason)
    const checkedAt = '2026-10-25T09:00:00Z'
    expect(imageUpdateTooltip({ ...update, checked_at: checkedAt })).toContain(
      new Date(checkedAt).toLocaleString(),
    )
  })

  test('error fallback', () => {
    const fallback = 'upstream-error-sentinel'
    for (const status of [401, 403, 409, 413]) {
      expect(workspaceErrorMessage(status, fallback)).not.toBe('')
      expect(workspaceErrorMessage(status, fallback)).not.toContain(fallback)
    }
    expect(workspaceErrorMessage(500, fallback)).toBe(fallback)
  })
})
