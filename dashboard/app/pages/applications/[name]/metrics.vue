<script setup lang="ts">
import { formatBytes, formatPercent } from '~/utils/format'

/** What the proxy saw of the application, and what its replicas used: both over the last hour, day or week. */
const { name, detail, gone, hasActive, isStatic } = useApplication()

const history = useMetricsHistory(name, () => hasActive.value && !gone.value && !isStatic.value)
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
      <div v-else-if="history.history.value" class="divide-y divide-line">
        <MetricsHistoryChart :history="history.history.value" metric="cpu_percent" :range="history.range.value" label="CPU" :format="formatPercent" />
        <MetricsHistoryChart :history="history.history.value" metric="memory_bytes" :range="history.range.value" label="Memory" :format="formatBytes" />
      </div>
    </UiPanel>

    <p v-if="isStatic" class="text-xs text-fg-subtle">
      A folder served by the proxy has no containers, so there is no CPU or memory to chart.
    </p>
  </div>
</template>
