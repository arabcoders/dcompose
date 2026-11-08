import { expect, test } from 'bun:test'
import { loadHighlighter } from '../app/utils/highlight'

test('shared highlighter', () => {
  expect(loadHighlighter()).toBe(loadHighlighter())
})

test('yaml and env', async () => {
  const highlighter = await loadHighlighter()
  const yaml = highlighter.highlight('services:\n  web:\n    image: nginx:alpine\n', {
    language: 'yaml',
  }).value
  expect(yaml).toContain('hljs-attr')
  expect(yaml).toContain('\n  ')
  const env = highlighter.highlight('API_KEY="example"\n', { language: 'ini' }).value
  expect(env).toContain('hljs-attr')
  expect(env).toContain('hljs-string')
})

test('escaped markup', async () => {
  const highlighter = await loadHighlighter()
  for (const language of ['yaml', 'ini']) {
    const html = highlighter.highlight('VALUE=<script>alert(1)</script>\n', { language }).value
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  }
})
