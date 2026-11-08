import { describe, expect, test } from 'bun:test'
import { nodeConnectionRequest } from '../app/utils/settings'

describe('Settings', () => {
  const node = {
    id: 'local',
    name: ' Local ',
    url: ' https://agent:9443 ',
    token: '',
    fingerprint: ' abc123 ',
    public_host: ' agent.example.com ',
    has_token: true,
  }

  test('edit shape', () => {
    expect(nodeConnectionRequest(node)).toEqual({
      name: 'Local',
      url: 'https://agent:9443',
      token: '',
      fingerprint: 'abc123',
      public_host: 'agent.example.com',
    })
  })

  test('name and token whitespace', () => {
    expect(
      nodeConnectionRequest({ ...node, name: ' Living room / #2 ', token: ' secret ' }),
    ).toEqual({
      name: 'Living room / #2',
      url: 'https://agent:9443',
      token: ' secret ',
      fingerprint: 'abc123',
      public_host: 'agent.example.com',
    })
  })
})
