import type { MaybeRefOrGetter } from 'vue'
import type { MetricsHistory, MetricsRange } from '~/types/api'

/** The agent samples every 30 seconds; asking more often would only repeat the last answer. */
const REFRESH_INTERVAL_MS = 30_000

/**
 * The sampled history the agent keeps (seven days, 30-second samples): one
 * request per range change and every 30 seconds after that. The live
 * point-in-time numbers next to it come from useLiveMetrics.
 */
export function useMetricsHistory(application: MaybeRefOrGetter<string>, enabled: MaybeRefOrGetter<boolean> = true) {
  const agent = useAgent()
  const range = ref<MetricsRange>('1h')

  const polling = usePolling<MetricsHistory>(signal => agent.get<MetricsHistory>(
    `/applications/${encodeURIComponent(toValue(application))}/metrics/history`,
    { query: { since: range.value }, signal },
  ), { interval: REFRESH_INTERVAL_MS, enabled })

  watch([range, () => toValue(application)], () => void polling.reset())

  /** 409 NOT_DEPLOYED: nothing is deployed, so there is nothing to have sampled. */
  const unavailable = computed(() => polling.error.value?.code === 'NOT_DEPLOYED')
  /** An agent from before the history endpoint existed. */
  const unsupported = computed(() => polling.error.value?.code === 'ENDPOINT_NOT_FOUND')

  return {
    range,
    history: polling.data,
    error: polling.error,
    loading: polling.loading,
    refreshing: polling.refreshing,
    unavailable,
    unsupported,
    refresh: polling.refresh,
  }
}
