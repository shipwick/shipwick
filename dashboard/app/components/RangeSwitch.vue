<script setup lang="ts">
import type { MetricsRange } from '~/types/api'
import { METRICS_RANGES } from '~/utils/metricsHistory'

/** 1h / 24h / 7d: the window of a chart, the same control on the metrics history and on traffic. */
const range = defineModel<MetricsRange>({ required: true })
</script>

<template>
  <div role="group" aria-label="Range" class="inline-flex overflow-hidden rounded-sm border border-line-strong">
    <button
      v-for="r in METRICS_RANGES"
      :key="r"
      type="button"
      class="mono h-6 border-l border-line-strong px-2 text-xs first:border-l-0"
      :class="range === r ? 'bg-active font-medium text-fg' : 'text-fg-muted hover:bg-hover hover:text-fg'"
      :aria-pressed="range === r"
      @click="range = r"
    >
      {{ r }}
    </button>
  </div>
</template>
