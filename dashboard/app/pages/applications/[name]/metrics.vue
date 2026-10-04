<script setup lang="ts">
import { formatBytes, formatPercent } from '~/utils/format'
import { DAEMON_USAGE_NOTICE, limitsNotice, usageIsTheDaemons, withoutUnenforced } from '~/utils/limits'

/** What the proxy saw of the application, and what its replicas used: both over the last hour, day or week. */
const { name, detail, gone, hasActive, isStatic } = useApplication()

const history = useMetricsHistory(name, () => hasActive.value && !gone.value && !isStatic.value)

// The history carries the limits deploy.yaml asks for and not whether Docker
// applies them; the server says that. A limit nothing is held to is not a
// line in a chart, and an agent before 0.8, which does not say, changes nothing.
const server = useServerInfo()
const unenforced = computed(() => server.data.value?.docker?.unenforced_limits)
const daemonUsage = computed(() => usageIsTheDaemons(unenforced.value))
const charted = computed(() => (history.history.value ? withoutUnenforced(history.history.value, unenforced.value) : null))
const notice = computed(() => {
  const limits = history.history.value?.limits
  return limits ? limitsNotice(unenforced.value, { memory: limits.memory_bytes, cpu: limits.cpu }) : ''
})
</script>

<template>
  <div class="space-y-8">
    <!-- A static application has traffic too: the proxy answers for it. -->
    <TrafficPanel :application="name" :routed="Boolean(detail.domain)" :enabled="hasActive && !gone" />

    <!-- The sampled history: per replica, average CPU and peak memory per step, refreshed every 30s -->
    <UiPanel v-if="!isStatic" title="CPU and memory" :meta="history.history.value ? `one point every ${history.history.value.step}` : null">
      <template #actions>
        <RangeSwitch v-model="history.range.value" />
      </template>
      <EmptyState v-if="!hasActive || history.unavailable.value" title="Nothing is deployed">
        The history starts with the first successful deployment.
      </EmptyState>
      <EmptyState v-else-if="history.unsupported.value" title="This agent keeps no history">
        The history of CPU and memory needs a newer agent. Upgrade it to see them over time.
      </EmptyState>
      <div v-else-if="history.loading.value && !history.history.value" class="space-y-3 px-4 py-3" aria-busy="true">
        <span class="skeleton h-[7.5rem] w-full" />
        <span class="skeleton h-[7.5rem] w-full" />
      </div>
      <ErrorState
        v-else-if="history.error.value && !history.history.value"
        :error="history.error.value"
        subject="the history"
        :retrying="history.refreshing.value"
        @retry="history.refresh()"
      />
      <EmptyState v-else-if="history.history.value && daemonUsage" title="Not measured per replica">
        {{ DAEMON_USAGE_NOTICE }} {{ notice }}
        <NuxtLink to="/servers" class="link">
          Server status
        </NuxtLink>
      </EmptyState>
      <div v-else-if="charted" class="divide-y divide-line">
        <p v-if="notice" class="flex items-start gap-2 px-4 py-2 text-xs text-warn">
          <UiIcon name="alert" :size="14" class="mt-px" />
          <span class="min-w-0">{{ notice }}</span>
        </p>
        <MetricsHistoryChart :history="charted" metric="cpu_percent" :range="history.range.value" label="CPU" :format="formatPercent" />
        <MetricsHistoryChart :history="charted" metric="memory_bytes" :range="history.range.value" label="Memory" :format="formatBytes" />
      </div>
    </UiPanel>

    <p v-if="isStatic" class="text-xs text-fg-subtle">
      A folder served by the proxy has no containers, so there is no CPU or memory to chart.
    </p>
  </div>
</template>
