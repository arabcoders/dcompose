import type { LogStreamEvent } from '../types'

export class LogStreamParser {
  private buffer = ''
  private id: number | null = null
  private event = ''
  private data: Array<string> = []

  feed(chunk: string): Array<LogStreamEvent> {
    this.buffer += chunk
    const events: Array<LogStreamEvent> = []
    let boundary = this.buffer.indexOf('\n')
    while (0 <= boundary) {
      let line = this.buffer.slice(0, boundary)
      this.buffer = this.buffer.slice(boundary + 1)
      if (line.endsWith('\r')) {
        line = line.slice(0, -1)
      }
      if (!line) {
        if (this.data.length) {
          events.push({ id: this.id, event: this.event || 'message', data: this.data.join('\n') })
        }
        this.id = null
        this.event = ''
        this.data = []
      } else if (!line.startsWith(':')) {
        const separator = line.indexOf(':')
        const field = 0 > separator ? line : line.slice(0, separator)
        const value = 0 > separator ? '' : line.slice(separator + 1).replace(/^ /u, '')
        if ('id' === field && /^\d+$/u.test(value) && Number.isSafeInteger(Number(value))) {
          this.id = Number(value)
        } else if ('event' === field) {
          this.event = value
        } else if ('data' === field) {
          this.data.push(value)
        }
      }
      boundary = this.buffer.indexOf('\n')
    }
    return events
  }
}
