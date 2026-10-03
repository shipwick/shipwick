import type { MaybeRefOrGetter } from 'vue'
import type { Traffic, TrafficRange } from '~/types/api'

/** The agent writes a minute down when it has ended; asking more often than this shows nothing new but the current one. */
const REFRESH_INTERVAL_MS = 30_000
/** An agent that records no traffic will not start to while the page is open: look again rarely. */
const UNAVAILABLE_RETRY_MS = 5 * 60_000

/**
 * What the proxy's access log says about an application's requests: one
 * request per range change and every 30 seconds after that.
 */
export function useTraffic(application: MaybeRefOrGetter<string>, enabled: MaybeRefOrGetter<boolean> = true) {
  const agent = useAgent()
  const range = ref<TrafficRange>('1h')
  const quiet = ref(false)

  const polling = usePolling<Traffic>(signal => agent.get<Traffic>(
    `/applications/${encodeURIComponent(toValue(application))}/traffic`,
    { query: { since: range.value }, signal },
  ), { interval: () => (quiet.value ? UNAVAILABLE_RETRY_MS : REFRESH_INTERVAL_MS), enabled })

  /** 409 TRAFFIC_UNAVAILABLE: there is no access log to read. The agent's message says why. */
  const unavailable = computed(() => (polling.error.value?.code === 'TRAFFIC_UNAVAILABLE' ? polling.error.value : null))
  /** An agent from before it recorded traffic: there is nothing to show, not even an explanation. */
  const unsupported = computed(() => polling.error.value?.code === 'ENDPOINT_NOT_FOUND')

  watch([unavailable, unsupported], ([none, old]) => {
    quiet.value = none !== null || old
  })
  watch([range, () => toValue(application)], () => void polling.reset())

  return {
    range,
    traffic: polling.data,
    error: polling.error,
    loading: polling.loading,
    refreshing: polling.refreshing,
    unavailable,
    unsupported,
    refresh: polling.refresh,
  }
}
