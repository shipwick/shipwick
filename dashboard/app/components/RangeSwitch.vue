<script setup lang="ts">
import type { MetricsRange } from '~/types/api'
import { rovingTabStop } from '~/utils/focus'
import { METRICS_RANGES } from '~/utils/metricsHistory'

/** 1h / 24h / 7d: the window of a chart, the same control on the metrics history and on traffic. */
const range = defineModel<MetricsRange>({ required: true })

// One of three, always one: a radio group, moved through with the arrows.
const group = ref<HTMLElement | null>(null)
const tabStop = computed(() => rovingTabStop(METRICS_RANGES.length, METRICS_RANGES.indexOf(range.value)))
const { onKeydown } = useRovingFocus(group, { selector: '[role="radio"]', onMove: (index) => { range.value = METRICS_RANGES[index]! } })
</script>

<template>
  <!-- Not clipped to its rounded corners: the focus ring of a choice lies outside it. -->
  <div ref="group" role="radiogroup" aria-label="Range" class="inline-flex rounded-sm border border-line-strong" @keydown="onKeydown">
    <button
      v-for="(r, index) in METRICS_RANGES"
      :key="r"
      type="button"
      role="radio"
      :aria-checked="range === r"
      :tabindex="index === tabStop ? 0 : -1"
      class="target mono h-6 border-l border-line-strong px-2 text-xs first:rounded-l-[3px] first:border-l-0 last:rounded-r-[3px] focus-visible:relative"
      :class="range === r ? 'bg-active font-medium text-fg' : 'text-fg-muted hover:bg-hover hover:text-fg'"
      @click="range = r"
    >
      {{ r }}
    </button>
  </div>
</template>
