import { describe, expect, it } from 'vitest'
import { NdjsonSplitter, parseLogLine, readLogStream } from '../app/utils/ndjson'

const encoder = new TextEncoder()
const bytes = (text: string) => encoder.encode(text)

function line(message: string, replica = 1): string {
  return JSON.stringify({ replica, container: `shipwick_app_1_${replica}`, stream: 'stdout', time: '2026-03-01T10:00:00Z', message })
}

function streamOf(chunks: Uint8Array[]): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(chunk)
      controller.close()
    },
  })
}

describe('NdjsonSplitter', () => {
  it('returns complete lines and holds back the partial one', () => {
    const s = new NdjsonSplitter()
    expect(s.push(bytes('{"a":1}\n{"b":2}\n{"c"'))).toEqual(['{"a":1}', '{"b":2}'])
    expect(s.push(bytes(':3}\n'))).toEqual(['{"c":3}'])
    expect(s.flush()).toEqual([])
  })

  it('handles a chunk boundary exactly at the newline', () => {
    const s = new NdjsonSplitter()
    expect(s.push(bytes('{"a":1}'))).toEqual([])
    expect(s.push(bytes('\n'))).toEqual(['{"a":1}'])
    expect(s.push(bytes('{"b":2}\n'))).toEqual(['{"b":2}'])
  })

  it('handles one byte per chunk', () => {
    const s = new NdjsonSplitter()
    const out: string[] = []
    for (const b of bytes('{"a":1}\n{"b":"ü"}\n')) out.push(...s.push(new Uint8Array([b])))
    expect(out).toEqual(['{"a":1}', '{"b":"ü"}'])
  })

  it('reassembles a UTF-8 sequence split across chunks', () => {
    const full = bytes('{"m":"héllo → ✓ 🚀"}\n')
    // Cut inside the 4-byte rocket and inside the 2-byte é.
    const rocket = full.indexOf(0xF0)
    const e = full.indexOf(0xC3)
    const s = new NdjsonSplitter()
    const out = [
      ...s.push(full.slice(0, e + 1)),
      ...s.push(full.slice(e + 1, rocket + 2)),
      ...s.push(full.slice(rocket + 2)),
    ]
    expect(out).toEqual(['{"m":"héllo → ✓ 🚀"}'])
    expect(JSON.parse(out[0] ?? '').m).toBe('héllo → ✓ 🚀')
  })

  it('accepts CRLF and skips blank lines', () => {
    const s = new NdjsonSplitter()
    expect(s.push(bytes('{"a":1}\r\n\r\n\n{"b":2}\r\n'))).toEqual(['{"a":1}', '{"b":2}'])
  })

  it('flush returns a final line without a trailing newline, once', () => {
    const s = new NdjsonSplitter()
    expect(s.push(bytes('{"a":1}\n{"b":2}'))).toEqual(['{"a":1}'])
    expect(s.flush()).toEqual(['{"b":2}'])
    expect(s.flush()).toEqual([])
  })

  it('handles many lines in one chunk and empty chunks', () => {
    const s = new NdjsonSplitter()
    expect(s.push(new Uint8Array())).toEqual([])
    const many = Array.from({ length: 1000 }, (_, i) => `{"i":${i}}`).join('\n') + '\n'
    expect(s.push(bytes(many))).toHaveLength(1000)
  })
})

describe('parseLogLine', () => {
  it('parses a LogLine', () => {
    expect(parseLogLine(line('listening on :8080', 2))).toEqual({
      replica: 2,
      container: 'shipwick_app_1_2',
      stream: 'stdout',
      time: '2026-03-01T10:00:00Z',
      message: 'listening on :8080',
    })
  })

  it('normalizes unknown streams and missing fields', () => {
    expect(parseLogLine('{"message":"x","stream":"weird"}')).toEqual({ replica: 0, container: '', stream: 'stdout', time: '', message: 'x' })
    expect(parseLogLine('{"message":"x","stream":"stderr"}')?.stream).toBe('stderr')
  })

  it('rejects anything that is not a log line', () => {
    expect(parseLogLine('not json')).toBeNull()
    expect(parseLogLine('42')).toBeNull()
    expect(parseLogLine('null')).toBeNull()
    expect(parseLogLine('{"error":{"code":"NOT_FOUND"}}')).toBeNull()
  })
})

describe('readLogStream', () => {
  it('yields one batch per chunk and flushes the tail', async () => {
    const a = line('one')
    const b = line('two')
    const c = line('three')
    const stream = streamOf([bytes(`${a}\n${b.slice(0, 10)}`), bytes(`${b.slice(10)}\n`), bytes(c)])
    const batches: string[][] = []
    for await (const batch of readLogStream(stream)) batches.push(batch.map(l => l.message))
    expect(batches).toEqual([['one'], ['two'], ['three']])
  })

  it('skips garbage lines without stopping', async () => {
    const stream = streamOf([bytes(`${line('ok')}\n<html>\n${line('still ok')}\n`)])
    const messages: string[] = []
    for await (const batch of readLogStream(stream)) messages.push(...batch.map(l => l.message))
    expect(messages).toEqual(['ok', 'still ok'])
  })
})
