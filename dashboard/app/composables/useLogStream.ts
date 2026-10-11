import type { LogLine } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { AgentError as StreamError, isAbortError, toAgentError } from '~/utils/agentError'
import { STALE_AFTER_MS, boundaryDuplicates, endMeaning, isStale, latestTime, lineKey, resumeDelay, resumeQuery } from '~/utils/logResume'
import { LOG_SINCE_HEADER, readLogRecords } from '~/utils/ndjson'

export interface LogEntry extends LogLine {
  /** Monotonic id, unique within one viewer; the v-for key. */
  seq: number
  /** A message from the viewer itself ("stream ended"), not from a container. */
  notice?: boolean
}

export type LogStreamStatus
  = | 'idle'
    | 'connecting'
    /** Following: lines arrive as they are written. */
    | 'streaming'
    /** A one-off tail was loaded (follow is off). */
    | 'loaded'
    /** The agent closed the stream: every followed container is gone. */
    | 'ended'
    /** The stream was lost — the connection cut, or the agent restarting — and is being taken up where it stopped. */
    | 'resuming'
    | 'error'

export interface LogStreamParams {
  application: string
  tail: number
  follow: boolean
}

/** Lines kept in memory. Older ones are dropped (and counted) beyond this. */
export const LOG_BUFFER_LINES = 5000

const FLUSH_INTERVAL_MS = 100
const AUTO_RECONNECT_DELAY_MS = 2000
/** A stream that lived this long earns a fresh automatic reconnect when it ends. */
const STABLE_STREAM_MS = 10_000
/** How long lines the agent may send again are skipped after a reconnect. */
const DEDUPE_WINDOW_MS = 5000
/** The stale check runs just past the limit, so that `isStale` is true when it looks. */
const STALE_CHECK_MS = STALE_AFTER_MS + 50

/** How a connection comes to be made. */
type Opening
  /** The first connection, or a manual one: a fresh tail, nothing to skip. */
  = | { kind: 'fresh' }
    /** After the agent ended the stream: a fresh tail that may repeat what is on screen, skipped for a moment. */
    | { kind: 'replay' }
    /** After the stream was lost: from the time the agent last told, through its 502 while it restarts. */
    | { kind: 'resume', attempt: number }

/**
 * Tails or follows the logs of one application through the NDJSON endpoint.
 *
 * When a followed stream ends by itself because every followed container is
 * gone (a deployment replaced them) it reconnects once after two seconds; if
 * that stream ends right away too, it stays ended and leaves "Reconnect" to
 * the user. When the stream is lost — the connection cut, or the agent saying
 * it is restarting — it is taken up where it stopped: with `since` set to the
 * time the agent last told, retried for about half a minute, the one line at
 * the boundary dropped. An agent before 0.9 does not know `since` and sends
 * its tail again; what is on screen is then skipped for a moment, as before.
 */
export function useLogStream() {
  const agent = useAgent()

  const lines = shallowRef<LogEntry[]>([])
  const status = ref<LogStreamStatus>('idle')
  const error = shallowRef<AgentError | null>(null)
  const dropped = ref(0)
  /** True while waiting out the delay before the automatic reconnect. */
  const reconnecting = ref(false)
  /**
   * The agent writes a heartbeat into a quiet stream; 45 seconds without a
   * line or a heartbeat means the stream is probably cut and nothing said so.
   * Only once a heartbeat has been heard on this connection: an agent before
   * 0.9 sends none, and its quiet is not a fault.
   */
  const stale = ref(false)

  let params: LogStreamParams | null = null
  let controller: AbortController | null = null
  let seq = 0
  let pending: LogEntry[] = []
  let flushTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let autoReconnectAvailable = true
  /**
   * After an automatic reconnect the agent replays each replica's tail. If the
   * containers are in fact still the same (the agent restarted, the network
   * blipped), those lines are already on screen: skip them for a moment. After
   * a resume the agent sends the line at the boundary again: that one only.
   */
  let alreadyShown: Set<string> | null = null
  let dedupeTimer: ReturnType<typeof setTimeout> | null = null
  /** The latest time the agent told, from its lines and heartbeats: where a lost stream is taken up from. */
  let latest = ''
  let lastHeardAt = 0
  let heartbeats = false
  let staleTimer: ReturnType<typeof setTimeout> | null = null

  function flush() {
    flushTimer = null
    if (pending.length === 0) return
    let next = lines.value.concat(pending)
    pending = []
    if (next.length > LOG_BUFFER_LINES) {
      dropped.value += next.length - LOG_BUFFER_LINES
      next = next.slice(next.length - LOG_BUFFER_LINES)
    }
    lines.value = next
  }

  function push(batch: LogLine[]) {
    for (const line of batch) {
      if (alreadyShown?.has(lineKey(line))) continue
      pending.push({ ...line, seq: ++seq })
    }
    // While the tab is hidden timers are throttled; do not let the queue grow without bound.
    if (pending.length > LOG_BUFFER_LINES) {
      dropped.value += pending.length - LOG_BUFFER_LINES
      pending = pending.slice(pending.length - LOG_BUFFER_LINES)
    }
    if (flushTimer === null) flushTimer = setTimeout(flush, FLUSH_INTERVAL_MS)
  }

  function notice(message: string) {
    push([{ replica: 0, container: '', stream: 'stdout', time: new Date().toISOString(), message }])
    const last = pending[pending.length - 1]
    if (last) last.notice = true
  }

  function skipForAMoment(keys: Set<string>) {
    alreadyShown = keys
    dedupeTimer = setTimeout(() => {
      dedupeTimer = null
      alreadyShown = null
    }, DEDUPE_WINDOW_MS)
  }

  function clearStaleTimer() {
    if (staleTimer !== null) {
      clearTimeout(staleTimer)
      staleTimer = null
    }
  }

  /** A line or a heartbeat arrived: the stream is alive. */
  function heard() {
    lastHeardAt = Date.now()
    stale.value = false
    clearStaleTimer()
    if (heartbeats) {
      staleTimer = setTimeout(() => {
        staleTimer = null
        stale.value = isStale(lastHeardAt, Date.now())
      }, STALE_CHECK_MS)
    }
  }

  function cancelTimers() {
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    reconnecting.value = false
    if (dedupeTimer !== null) {
      clearTimeout(dedupeTimer)
      dedupeTimer = null
    }
    alreadyShown = null
    clearStaleTimer()
    stale.value = false
  }

  function stop() {
    cancelTimers()
    controller?.abort()
    controller = null
  }

  /** Takes a lost stream up after a wait, or gives up once the waits are used up. */
  function resumeLater(current: LogStreamParams, attempt: number) {
    const delay = resumeDelay(attempt)
    if (delay === null) {
      error.value = new StreamError(0, 'STREAM_LOST', 'The log stream was lost and the agent did not answer for half a minute. Reconnect starts over with a fresh tail.')
      status.value = 'error'
      notice('The stream could not be taken up again.')
      return
    }
    status.value = 'resuming'
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null
      void run(current, { kind: 'resume', attempt })
    }, delay)
  }

  async function run(current: LogStreamParams, opening: Opening) {
    stop()
    const own = new AbortController()
    controller = own
    params = current
    error.value = null
    status.value = opening.kind === 'resume' ? 'resuming' : 'connecting'
    const startedAt = Date.now()
    heartbeats = false
    if (opening.kind === 'replay') {
      flush()
      skipForAMoment(new Set(lines.value.filter(l => !l.notice).map(lineKey)))
    }

    let streaming = false
    try {
      if (!current.follow) {
        const result = await agent.get<LogLine[]>(`/applications/${encodeURIComponent(current.application)}/logs`, {
          query: { tail: Math.max(1, current.tail) },
          signal: own.signal,
        })
        if (own.signal.aborted) return
        push(result ?? [])
        status.value = 'loaded'
        return
      }

      const since = opening.kind === 'resume' ? latest : ''
      const response = await agent.stream(`/applications/${encodeURIComponent(current.application)}/logs`, {
        query: resumeQuery(current.tail, since),
        signal: own.signal,
      })
      if (own.signal.aborted) return
      if (!response.body) throw new Error('This browser does not support streaming responses')
      if (opening.kind === 'resume') {
        flush()
        const honoured = response.headers.get(LOG_SINCE_HEADER)
        // An agent that took `since` sends the line at that instant again, and nothing else twice.
        // One that did not know it sent its tail: what is on screen may come again.
        if (honoured !== null && since !== '') skipForAMoment(boundaryDuplicates(lines.value.filter(l => !l.notice), honoured || since))
        else skipForAMoment(new Set(lines.value.filter(l => !l.notice).map(lineKey)))
      }
      status.value = 'streaming'
      streaming = true
      heard()

      let end: string | null = null
      for await (const batch of readLogRecords(response.body)) {
        if (own.signal.aborted) return
        latest = latestTime(latest, batch)
        const received: LogLine[] = []
        for (const record of batch) {
          if (record.kind === 'line') received.push(record.line)
          else if (record.kind === 'heartbeat') heartbeats = true
          else end = record.reason
        }
        if (received.length > 0) push(received)
        heard()
      }
      if (own.signal.aborted) return
      clearStaleTimer()

      if (endMeaning(end) === 'resume') {
        // The agent is restarting: it is back in a moment, and the stream goes on from here.
        notice('The agent is restarting. Taking the stream up where it stopped…')
        resumeLater(current, 0)
        return
      }

      // The agent ended the stream on its own: every followed container is gone.
      status.value = 'ended'
      if (Date.now() - startedAt >= STABLE_STREAM_MS) autoReconnectAvailable = true
      if (autoReconnectAvailable) {
        autoReconnectAvailable = false
        notice('Stream ended: the containers were stopped or replaced. Reconnecting…')
        reconnecting.value = true
        reconnectTimer = setTimeout(() => {
          reconnectTimer = null
          reconnecting.value = false
          void run(current, { kind: 'replay' })
        }, AUTO_RECONNECT_DELAY_MS)
      }
      else {
        notice(opening.kind === 'replay'
          ? 'Stream ended again: nothing is running to follow.'
          : 'Stream ended: the containers were stopped or replaced.')
      }
    }
    catch (cause) {
      if (own.signal.aborted || isAbortError(cause)) return
      clearStaleTimer()
      if (streaming) {
        // Cut mid-stream: the proxy in front of the agent, or the network. The lines up to here are good; go on from them.
        notice('Connection lost. Taking the stream up where it stopped…')
        resumeLater(current, 0)
        return
      }
      if (opening.kind === 'resume') {
        // Not back yet: the proxy answers 502 while the agent restarts. Wait a little longer.
        resumeLater(current, opening.attempt + 1)
        return
      }
      if (opening.kind === 'replay') {
        // An agent before 0.9 ends a stream without a word when it restarts, and is not back two seconds later: keep trying, as after a cut.
        resumeLater(current, 0)
        return
      }
      error.value = toAgentError(cause)
      status.value = 'error'
    }
    finally {
      if (controller === own) controller = null
    }
  }

  /** Starts (or restarts) with new parameters and an empty buffer, so a new tail never duplicates lines. */
  function connect(next: LogStreamParams) {
    clear()
    autoReconnectAvailable = true
    return run(next, { kind: 'fresh' })
  }

  /** Manual "Reconnect": starts over with a fresh tail, so nothing is shown twice. */
  function reconnect() {
    if (!params) return Promise.resolve()
    clear()
    autoReconnectAvailable = true
    return run(params, { kind: 'fresh' })
  }

  function disconnect() {
    stop()
    if (status.value === 'connecting' || status.value === 'streaming' || status.value === 'resuming') status.value = 'idle'
  }

  function clear() {
    pending = []
    lines.value = []
    dropped.value = 0
    latest = ''
  }

  onScopeDispose(() => {
    stop()
    if (flushTimer !== null) clearTimeout(flushTimer)
  })

  return { lines, status, error, dropped, reconnecting, stale, connect, reconnect, disconnect, clear }
}
