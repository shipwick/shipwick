import type { MaybeRefOrGetter } from 'vue'
import type { ApplicationMetrics } from '~/types/api'
import { AgentError } from '~/utils/agentError'

/** About five minutes at one sample every five seconds. */
export const METRICS_HISTORY_SAMPLES = 60

const SAMPLE_INTERVAL_MS = 5000
/** While nothing is deployed, look again occasionally rather than every five seconds. */
const UNAVAILABLE_RETRY_MS = 60_000

export type MetricsAvailability
  = | 'loading'
    | 'available'
    /** 409 NOT_DEPLOYED: the application has no active deployment. (Stopped replicas report zeros, never an error.) */
    | 'unavailable'
    | 'error'

/**
 * The metrics endpoint returns one point-in-time sample; the short history
 * behind the sparklines is built here, in memory, while the page is open.
 */
export function useMetricsHistory(application: MaybeRefOrGetter<string>, enabled: MaybeRefOrGetter<boolean> = true) {
  const agent = useAgent()
  const samples = shallowRef<ApplicationMetrics[]>([])
  const availability = ref<MetricsAvailability>('loading')
  const reason = ref('')

  const polling = usePolling<ApplicationMetrics | null>(async (signal) => {
    try {
      const sample = await agent.get<ApplicationMetrics>(`/applications/${encodeURIComponent(toValue(application))}/metrics`, { signal })
      availability.value = 'available'
      reason.value = ''
      return sample
    }
    catch (error) {
      if (error instanceof AgentError && error.code === 'NOT_DEPLOYED') {
        availability.value = 'unavailable'
        reason.value = 'Nothing is deployed'
        return null
      }
      if (error instanceof AgentError) {
        availability.value = 'error'
        reason.value = error.message
      }
      throw error
    }
  }, {
    interval: () => (availability.value === 'unavailable' ? UNAVAILABLE_RETRY_MS : SAMPLE_INTERVAL_MS),
    enabled,
  })

  watch(polling.data, (sample) => {
    if (!sample) return
    const last = samples.value[samples.value.length - 1]
    if (last && last.collected_at === sample.collected_at) return
    samples.value = [...samples.value, sample].slice(-METRICS_HISTORY_SAMPLES)
  })

  watch(() => toValue(application), () => {
    samples.value = []
    availability.value = 'loading'
    void polling.reset()
  })

  const latest = computed(() => (availability.value === 'available' ? samples.value[samples.value.length - 1] ?? null : null))

  return { samples, latest, availability, reason }
}
