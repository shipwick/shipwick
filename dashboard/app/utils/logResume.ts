import type { LogLine } from '~/types/api'
import type { LogRecord } from '~/utils/ndjson'

/**
 * How a followed log is taken up after it was lost: the rules, pure, so they
 * are unit-tested. From 0.9 on the agent writes a heartbeat into a quiet
 * stream and a last line that says why it ended one, and takes `since=<time>`
 * to begin again at a time; the viewer uses them to reconnect without a line
 * lost or repeated. An older agent gives none of this, and the viewer falls
 * back to what it did before: a fresh tail, with what is on screen skipped
 * for a moment.
 */

/**
 * The waits before each try to take a lost stream up, in milliseconds: about
 * half a minute in all, which covers a restart of the agent and the proxy's
 * 502 while it is away. `shipwick logs -f` waits the same.
 */
export const RESUME_DELAYS_MS: readonly number[] = [1000, 2000, 4000, 8000, 16000]

/** The wait before try `attempt` (0-based), or null when the tries are used up. */
export function resumeDelay(attempt: number): number | null {
  return RESUME_DELAYS_MS[attempt] ?? null
}

/** A heartbeat tells a live stream from a dead one; this long without a line or a heartbeat and the stream is stale. */
export const STALE_AFTER_MS = 45_000

/** Whether a stream that has carried heartbeats has gone quiet for too long: it is probably cut, and no byte has said so. */
export function isStale(lastHeardAt: number, now: number): boolean {
  return now - lastHeardAt >= STALE_AFTER_MS
}

/**
 * The latest time the agent told, from its lines and its heartbeats: where
 * the stream is taken up from. A heartbeat counts, so a quiet application
 * does not have its whole tail replayed. Times that do not parse are ignored;
 * "" when nothing has been heard.
 */
export function latestTime(previous: string, records: readonly LogRecord[]): string {
  let latest = previous
  let latestMs = Date.parse(previous)
  for (const record of records) {
    const time = record.kind === 'line' ? record.line.time : record.kind === 'heartbeat' ? record.time : ''
    const ms = Date.parse(time)
    if (Number.isNaN(ms)) continue
    if (Number.isNaN(latestMs) || ms > latestMs) {
      latest = time
      latestMs = ms
    }
  }
  return latest
}

/** The parameters of the request that takes a stream up: at the time the agent last told, or, without one, the tail as before. */
export function resumeQuery(tail: number, since: string): { follow: true, since: string } | { follow: true, tail: number } {
  return since !== '' ? { follow: true, since } : { follow: true, tail }
}

/** One line's identity: the same container, time and text is the same line. */
export const lineKey = (line: Pick<LogLine, 'container' | 'time' | 'message'>) => `${line.container}|${line.time}|${line.message}`

/**
 * What a stream taken up with `since` repeats: Docker's `since` is inclusive,
 * so the line at that instant comes again. The lines on screen at that
 * instant, by identity, are what to drop once; everything later is new.
 */
export function boundaryDuplicates(shown: readonly Pick<LogLine, 'container' | 'time' | 'message'>[], since: string): Set<string> {
  const at = Date.parse(since)
  if (Number.isNaN(at)) return new Set()
  return new Set(shown.filter(line => Date.parse(line.time) === at).map(lineKey))
}

/**
 * What a stream's end means. `containers`: every followed container is gone,
 * a deployment replaced them; a stream from an agent before 0.9 ends without
 * a word, and that is the only thing it can mean there. `shutdown`: the agent
 * is restarting and is back in a moment: take the stream up where it stopped.
 */
export function endMeaning(reason: string | null): 'ended' | 'resume' {
  return reason === 'shutdown' ? 'resume' : 'ended'
}
