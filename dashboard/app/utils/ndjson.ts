import type { LogLine } from '~/types/api'

/**
 * Incremental splitter for newline-delimited JSON arriving as raw bytes.
 *
 * Network chunks ignore every boundary that matters to us: a chunk may end in
 * the middle of a line, or in the middle of a multi-byte UTF-8 sequence. The
 * streaming TextDecoder holds back incomplete sequences, and `rest` holds back
 * the incomplete line.
 */
export class NdjsonSplitter {
  private readonly decoder = new TextDecoder('utf-8')
  private rest = ''

  /** Feeds one chunk and returns the lines it completed (without terminators, blank lines dropped). */
  push(chunk: Uint8Array): string[] {
    return this.take(this.decoder.decode(chunk, { stream: true }))
  }

  /** Call once when the stream ends: returns a final line that had no trailing newline. */
  flush(): string[] {
    const lines = this.take(this.decoder.decode())
    const last = this.rest.trim()
    this.rest = ''
    if (last !== '') lines.push(last)
    return lines
  }

  private take(text: string): string[] {
    if (text === '') return []
    const parts = (this.rest + text).split('\n')
    this.rest = parts.pop() ?? ''
    const lines: string[] = []
    for (const part of parts) {
      const line = part.endsWith('\r') ? part.slice(0, -1) : part
      if (line.trim() !== '') lines.push(line)
    }
    return lines
  }
}

/** Parses one NDJSON record into a LogLine; returns null for anything that is not one. */
export function parseLogLine(raw: string): LogLine | null {
  let value: unknown
  try {
    value = JSON.parse(raw)
  }
  catch {
    return null
  }
  if (typeof value !== 'object' || value === null) return null
  const record = value as Record<string, unknown>
  if (typeof record.message !== 'string') return null
  return {
    replica: typeof record.replica === 'number' ? record.replica : 0,
    container: typeof record.container === 'string' ? record.container : '',
    stream: record.stream === 'stderr' ? 'stderr' : 'stdout',
    time: typeof record.time === 'string' ? record.time : '',
    message: record.message,
  }
}

/**
 * Reads a byte stream to its end, yielding parsed log lines in batches (one
 * batch per network chunk, so consumers can update the UI once per chunk).
 */
export async function* readLogStream(body: ReadableStream<Uint8Array>): AsyncGenerator<LogLine[], void, void> {
  const reader = body.getReader()
  const splitter = new NdjsonSplitter()
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      if (!value) continue
      const batch = toLogLines(splitter.push(value))
      if (batch.length > 0) yield batch
    }
    const tail = toLogLines(splitter.flush())
    if (tail.length > 0) yield tail
  }
  finally {
    reader.releaseLock()
  }
}

function toLogLines(raw: string[]): LogLine[] {
  const out: LogLine[] = []
  for (const line of raw) {
    const parsed = parseLogLine(line)
    if (parsed) out.push(parsed)
  }
  return out
}
