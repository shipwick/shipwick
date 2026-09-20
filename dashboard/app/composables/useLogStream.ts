import type { LogLine } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { isAbortError, toAgentError } from '~/utils/agentError'
import { readLogStream } from '~/utils/ndjson'

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

/**
 * Tails or follows the logs of one application through the NDJSON endpoint.
 *
 * When a followed stream ends by itself (a deployment replaced the containers)
 * it reconnects once after two seconds; if that stream ends right away too, it
 * stays ended and leaves "Reconnect" to the user.
 */
export function useLogStream() {
  const agent = useAgent()

  const lines = shallowRef<LogEntry[]>([])
  const status = ref<LogStreamStatus>('idle')
  const error = shallowRef<AgentError | null>(null)
  const dropped = ref(0)
  /** True while waiting out the delay before the automatic reconnect. */
  const reconnecting = ref(false)

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
   * blipped), those lines are already on screen: skip them for a moment.
   */
  let alreadyShown: Set<string> | null = null
  let dedupeTimer: ReturnType<typeof setTimeout> | null = null

  const keyOf = (line: LogLine) => `${line.container}|${line.time}|${line.message}`

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
      if (alreadyShown?.has(keyOf(line))) continue
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
  }

  function stop() {
    cancelTimers()
    controller?.abort()
    controller = null
  }

  async function run(current: LogStreamParams, automatic: boolean) {
    stop()
    const own = new AbortController()
    controller = own
    params = current
    error.value = null
    status.value = 'connecting'
    const startedAt = Date.now()
    if (automatic) {
      flush()
      alreadyShown = new Set(lines.value.filter(l => !l.notice).map(keyOf))
      dedupeTimer = setTimeout(() => {
        dedupeTimer = null
        alreadyShown = null
      }, 5000)
    }

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

      const response = await agent.stream(`/applications/${encodeURIComponent(current.application)}/logs`, {
        query: { follow: true, tail: current.tail },
        signal: own.signal,
      })
      if (own.signal.aborted) return
      if (!response.body) throw new Error('This browser does not support streaming responses')
      status.value = 'streaming'

      for await (const batch of readLogStream(response.body)) {
        if (own.signal.aborted) return
        push(batch)
      }
      if (own.signal.aborted) return

      // The agent ended the stream on its own.
      status.value = 'ended'
      if (Date.now() - startedAt >= STABLE_STREAM_MS) autoReconnectAvailable = true
      if (autoReconnectAvailable) {
        autoReconnectAvailable = false
        notice('Stream ended: the containers were stopped or replaced. Reconnecting…')
        reconnecting.value = true
        reconnectTimer = setTimeout(() => {
          reconnectTimer = null
          reconnecting.value = false
          void run(current, true)
        }, AUTO_RECONNECT_DELAY_MS)
      }
      else {
        notice(automatic
          ? 'Stream ended again: nothing is running to follow.'
          : 'Stream ended: the containers were stopped or replaced.')
      }
    }
    catch (cause) {
      if (own.signal.aborted || isAbortError(cause)) return
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
    return run(next, false)
  }

  /** Manual "Reconnect": starts over with a fresh tail, so nothing is shown twice. */
  function reconnect() {
    if (!params) return Promise.resolve()
    clear()
    autoReconnectAvailable = true
    return run(params, false)
  }

  function disconnect() {
    stop()
    if (status.value === 'connecting' || status.value === 'streaming') status.value = 'idle'
  }

  function clear() {
    pending = []
    lines.value = []
    dropped.value = 0
  }

  onScopeDispose(() => {
    stop()
    if (flushTimer !== null) clearTimeout(flushTimer)
  })

  return { lines, status, error, dropped, reconnecting, connect, reconnect, disconnect, clear }
}
