import type { MaybeRefOrGetter } from 'vue'
import type { AgentError } from '~/utils/agentError'
import { isAbortError, toAgentError } from '~/utils/agentError'

export interface PollingOptions<T> {
  /** Milliseconds between the end of one request and the start of the next. Default 5000. */
  interval?: MaybeRefOrGetter<number>
  /** Polling (and the initial load) only happens while this is true. Default true. */
  enabled?: MaybeRefOrGetter<boolean>
  /** Return true to stop polling for good after this result (e.g. a finished deployment). */
  until?: (data: T) => boolean
}

/**
 * Polls one resource.
 *
 * - At most one request is in flight; the next one is scheduled only after the
 *   previous one settled, so a slow agent is never piled onto.
 * - Pauses while the tab is hidden and refreshes at once when it becomes visible.
 * - Aborts and stops when the owning component goes away (route change).
 * - Keeps the last good data on screen when a refresh fails: no layout shift,
 *   and `error` tells the page to show a "stale" notice.
 */
export function usePolling<T>(fetcher: (signal: AbortSignal) => Promise<T>, options: PollingOptions<T> = {}) {
  const data = shallowRef<T | null>(null)
  const error = shallowRef<AgentError | null>(null)
  /** True until the first request settled (successfully or not). */
  const loading = ref(true)
  const refreshing = ref(false)
  const updatedAt = ref<number | null>(null)

  let controller: AbortController | null = null
  let inFlight: Promise<void> | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let disposed = false
  let finished = false

  const interval = () => Math.max(250, toValue(options.interval) ?? 5000)
  const enabled = () => toValue(options.enabled) ?? true

  function clearTimer() {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }

  function schedule() {
    clearTimer()
    if (disposed || finished || !enabled() || document.hidden) return
    timer = setTimeout(() => void refresh(), interval())
  }

  function refresh(): Promise<void> {
    if (disposed) return Promise.resolve()
    if (inFlight) return inFlight
    clearTimer()

    const own = new AbortController()
    controller = own
    refreshing.value = true

    inFlight = fetcher(own.signal)
      .then((result) => {
        if (own.signal.aborted) return
        data.value = result
        error.value = null
        updatedAt.value = Date.now()
        if (options.until?.(result)) finished = true
      })
      .catch((cause: unknown) => {
        if (own.signal.aborted || isAbortError(cause)) return
        error.value = toAgentError(cause)
        // An expired session is handled globally (redirect to login): stop asking.
        if (error.value.status === 401) finished = true
      })
      .finally(() => {
        if (controller === own) {
          controller = null
          inFlight = null
          refreshing.value = false
          loading.value = false
          schedule()
        }
      })
    return inFlight
  }

  /** Drops the current data and starts over, e.g. when the resource key changed. */
  function reset(): Promise<void> {
    controller?.abort()
    controller = null
    inFlight = null
    finished = false
    data.value = null
    error.value = null
    updatedAt.value = null
    loading.value = true
    refreshing.value = false
    return enabled() ? refresh() : Promise.resolve()
  }

  /** Resumes polling after `until` stopped it. */
  function resume() {
    finished = false
    void refresh()
  }

  function onVisibilityChange() {
    if (document.hidden) {
      clearTimer()
      return
    }
    if (finished || !enabled()) return
    const age = updatedAt.value === null ? Infinity : Date.now() - updatedAt.value
    if (age >= interval()) void refresh()
    else schedule()
  }

  onMounted(() => {
    document.addEventListener('visibilitychange', onVisibilityChange)
    if (enabled()) void refresh()
    else loading.value = false
  })

  watch(() => enabled(), (on) => {
    if (on) void refresh()
    else clearTimer()
  })

  // A shorter interval should take effect now, not after the current long wait.
  watch(() => interval(), () => {
    if (timer !== null) schedule()
  })

  onScopeDispose(() => {
    disposed = true
    clearTimer()
    controller?.abort()
    if (import.meta.client) document.removeEventListener('visibilitychange', onVisibilityChange)
  })

  return { data, error, loading, refreshing, updatedAt, refresh, reset, resume }
}
