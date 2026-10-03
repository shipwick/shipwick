<script setup lang="ts">
import type { TrafficSeries } from '~/components/TrafficChart.vue'
import { formatBytes, formatDuration } from '~/utils/format'
import { errorShare, formatCount, formatLatency, hasTraffic, toTrafficChart } from '~/utils/traffic'

/**
 * What the proxy saw of the application: requests per step with the 5xx
 * among them, the 95th percentile of their durations, the window's totals,
 * and the most recent requests one by one. A static application has traffic
 * like any other; it is the proxy that answers for it.
 */
const props = defineProps<{
  application: string
  /** The application has a domain: without one nothing reaches it through the proxy. */
  routed: boolean
  enabled: boolean
}>()

const traffic = useTraffic(() => props.application, () => props.enabled)

// Redrawn when an answer arrives, not with the clock: the window ends at the moment of the answer.
const chart = computed(() => (traffic.traffic.value ? toTrafficChart(traffic.traffic.value, traffic.range.value) : null))
const totals = computed(() => traffic.traffic.value?.totals ?? null)
const any = computed(() => hasTraffic(traffic.traffic.value))

const requestSeries = computed<TrafficSeries[]>(() => {
  const c = chart.value
  const t = totals.value
  if (!c || !t) return []
  return [
    { key: 'requests', label: 'Requests', color: 'var(--series-1)', segments: [c.requests], summary: formatCount(t.requests) },
    { key: '5xx', label: '5xx', color: 'var(--danger-dot)', segments: [c.errors], summary: formatCount(t.status_5xx) },
  ]
})

const latencySeries = computed<TrafficSeries[]>(() => {
  const c = chart.value
  const t = totals.value
  if (!c || !t) return []
  return [{ key: 'p95', label: 'p95', color: 'var(--series-7)', segments: c.p95, summary: formatLatency(t.p95_ms) }]
})

const step = computed(() => (traffic.traffic.value ? formatDuration(traffic.traffic.value.step_seconds * 1000) : null))

/** The requests are asked for only while their table is open. */
const showRequests = ref(false)
watch(() => props.application, () => {
  showRequests.value = false
})
</script>

<template>
  <!-- An agent from before it recorded traffic: nothing to show, so nothing is shown. -->
  <UiPanel v-if="!traffic.unsupported.value" title="Traffic" :meta="step ? `per ${step}` : null">
    <template v-if="!traffic.unavailable.value" #actions>
      <RangeSwitch v-model="traffic.range.value" />
    </template>

    <!-- 409 TRAFFIC_UNAVAILABLE: said once, here, instead of an empty chart and an empty table each saying it. -->
    <EmptyState v-if="traffic.unavailable.value" title="This server records no traffic">
      {{ traffic.unavailable.value.message.charAt(0).toUpperCase() + traffic.unavailable.value.message.slice(1) }}.
    </EmptyState>
    <div v-else-if="traffic.loading.value && !traffic.traffic.value" class="space-y-3 px-4 py-3" aria-busy="true">
      <span class="skeleton h-8 w-full" />
      <span class="skeleton h-[7.5rem] w-full" />
    </div>
    <ErrorState
      v-else-if="traffic.error.value && !traffic.traffic.value"
      :error="traffic.error.value"
      subject="the traffic"
      :retrying="traffic.refreshing.value"
      @retry="traffic.refresh()"
    />
    <div v-else-if="chart && totals" class="divide-y divide-line">
      <dl class="grid grid-cols-2 gap-px bg-line sm:grid-cols-3 lg:grid-cols-6">
        <div class="bg-bg px-4 py-2.5">
          <dt class="label">
            Requests
          </dt>
          <dd class="mono mt-0.5">
            {{ formatCount(totals.requests) }}
          </dd>
        </div>
        <div class="bg-bg px-4 py-2.5">
          <dt class="label">
            5xx
          </dt>
          <dd class="mono mt-0.5" :class="totals.status_5xx > 0 ? 'text-danger' : ''" :title="`${formatCount(totals.status_5xx)} of ${formatCount(totals.requests)} requests`">
            {{ errorShare(totals) }}
            <span v-if="totals.status_5xx > 0" class="text-fg-subtle">· {{ formatCount(totals.status_5xx) }}</span>
          </dd>
        </div>
        <div class="bg-bg px-4 py-2.5">
          <dt class="label">
            4xx
          </dt>
          <dd class="mono mt-0.5">
            {{ formatCount(totals.status_4xx) }}
          </dd>
        </div>
        <div class="bg-bg px-4 py-2.5">
          <dt class="label">
            p50 · p95
          </dt>
          <dd class="mono mt-0.5">
            {{ any ? `${formatLatency(totals.p50_ms)} · ${formatLatency(totals.p95_ms)}` : '—' }}
          </dd>
        </div>
        <div class="bg-bg px-4 py-2.5">
          <dt class="label">
            p99
          </dt>
          <dd class="mono mt-0.5">
            {{ any ? formatLatency(totals.p99_ms) : '—' }}
          </dd>
        </div>
        <div class="bg-bg px-4 py-2.5">
          <dt class="label">
            Sent
          </dt>
          <dd class="mono mt-0.5" title="Response bodies as sent, after compression">
            {{ formatBytes(totals.bytes) }}
          </dd>
        </div>
      </dl>

      <p v-if="!any" class="px-4 py-3 text-fg-muted">
        <template v-if="props.routed">
          No request reached this application in the last {{ traffic.range.value }}.
        </template>
        <template v-else>
          This application has no domain, so nothing reaches it through the proxy and there is no traffic to record.
        </template>
      </p>
      <template v-else>
        <TrafficChart
          label="Requests"
          :series="requestSeries"
          :start="chart.start"
          :end="chart.end"
          :step-ms="chart.stepMs"
          :max="chart.maxRequests"
          :range="traffic.range.value"
          :format="v => formatCount(v)"
        />
        <TrafficChart
          label="Duration"
          :series="latencySeries"
          :start="chart.start"
          :end="chart.end"
          :step-ms="chart.stepMs"
          :max="chart.maxP95"
          :range="traffic.range.value"
          :format="formatLatency"
        />
      </template>

      <div v-if="props.routed">
        <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 bg-subtle px-4 py-2">
          <span class="label">Recent requests</span>
          <UiButton size="sm" variant="ghost" :aria-expanded="showRequests" @click="showRequests = !showRequests">
            {{ showRequests ? 'Hide' : 'Show' }}
          </UiButton>
        </div>
        <RecentRequests v-if="showRequests" :application="props.application" class="border-t border-line" />
      </div>
    </div>
  </UiPanel>
</template>
