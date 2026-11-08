import { describe, expect, test } from 'bun:test'
import { apiProxyTarget } from '../api-proxy'

describe('API proxy target', () => {
  test.each([
    ['http://127.0.0.1:8000', 'http://127.0.0.1:8000/'],
    ['http://127.0.0.1:8000/', 'http://127.0.0.1:8000/'],
    ['http://127.0.0.1:8000/api/', 'http://127.0.0.1:8000/'],
    ['http://127.0.0.1:8000/api', 'http://127.0.0.1:8000/'],
    ['https://backend.example/prefix/api/', 'https://backend.example/prefix/'],
    ['https://backend.example/prefix/api', 'https://backend.example/prefix/'],
    ['https://backend.example/prefix/', 'https://backend.example/prefix/'],
  ])('%s', (input, expected) => {
    expect(apiProxyTarget(input)).toBe(expected)
  })
})
