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

/**
 * One record of a followed log. Besides its lines, an agent from 0.9 on writes
 * a heartbeat after 20 seconds without a line, and a last line that says why
 * it ended the stream; neither has a `message`, which is how they are told
 * from a line. Older agents write lines only.
 */
export type LogRecord
  = | { kind: 'line', line: LogLine }
    | { kind: 'heartbeat', time: string }
    | { kind: 'end', reason: 'containers' | 'shutdown' | string }

/** The response header of a followed log asked for `since`: the time the stream begins at, as the agent read it. Absent on agents before 0.9. */
export const LOG_SINCE_HEADER = 'x-shipwick-log-since'

/** Parses one NDJSON record; returns null for anything that is neither a line, a heartbeat nor an end. */
export function parseLogRecord(raw: string): LogRecord | null {
  let value: unknown
  try {
    value = JSON.parse(raw)
  }
  catch {
    return null
  }
  if (typeof value !== 'object' || value === null) return null
  const record = value as Record<string, unknown>
  if (typeof record.message === 'string') {
    return {
      kind: 'line',
      line: {
        replica: typeof record.replica === 'number' ? record.replica : 0,
        container: typeof record.container === 'string' ? record.container : '',
        stream: record.stream === 'stderr' ? 'stderr' : 'stdout',
        time: typeof record.time === 'string' ? record.time : '',
        message: record.message,
      },
    }
  }
  if (record.heartbeat === true) return { kind: 'heartbeat', time: typeof record.time === 'string' ? record.time : '' }
  if (typeof record.end === 'string' && record.end !== '') return { kind: 'end', reason: record.end }
  return null
}

/** Parses one NDJSON record into a LogLine; returns null for anything that is not one, the heartbeat and end lines included. */
export function parseLogLine(raw: string): LogLine | null {
  const record = parseLogRecord(raw)
  return record?.kind === 'line' ? record.line : null
}

/**
 * Reads a byte stream to its end, yielding parsed records in batches (one
 * batch per network chunk, so consumers can update the UI once per chunk).
 */
export async function* readLogRecords(body: ReadableStream<Uint8Array>): AsyncGenerator<LogRecord[], void, void> {
  const reader = body.getReader()
  const splitter = new NdjsonSplitter()
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      if (!value) continue
      const batch = toRecords(splitter.push(value))
      if (batch.length > 0) yield batch
    }
    const tail = toRecords(splitter.flush())
    if (tail.length > 0) yield tail
  }
  finally {
    reader.releaseLock()
  }
}

/** Reads a byte stream to its end, yielding its log lines only, in batches. */
export async function* readLogStream(body: ReadableStream<Uint8Array>): AsyncGenerator<LogLine[], void, void> {
  for await (const batch of readLogRecords(body)) {
    const lines = batch.flatMap(record => (record.kind === 'line' ? [record.line] : []))
    if (lines.length > 0) yield lines
  }
}

function toRecords(raw: string[]): LogRecord[] {
  const out: LogRecord[] = []
  for (const line of raw) {
    const parsed = parseLogRecord(line)
    if (parsed) out.push(parsed)
  }
  return out
}
