<script setup lang="ts">
import type { Alert, DiskUsage } from '~/types/api'
import { diskDisplay } from '~/utils/alerts'
import type { Tone } from '~/utils/status'

/** How full the disk that holds the agent's data is. Its color is the agent's judgement: a `disk` alert, when there is one. */
const props = defineProps<{
  /** Null where the agent cannot measure it. */
  disk: DiskUsage | null
  alerts: Alert[]
}>()

const display = computed(() => diskDisplay(props.disk, props.alerts))

const FILL: Record<Tone, string> = { ok: 'bg-fg-subtle', warn: 'bg-warn-dot', danger: 'bg-danger-dot', muted: 'bg-muted-dot' }
const TEXT: Record<Tone, string> = { ok: '', warn: 'text-warn', danger: 'text-danger', muted: '' }
</script>

<template>
  <div v-if="display">
    <div class="flex flex-wrap items-baseline gap-x-3">
      <span class="mono" :class="TEXT[display.tone]">{{ display.percent }}%</span>
      <span class="text-fg-muted">{{ display.used }} · {{ display.free }}</span>
    </div>
    <div
      class="mt-1.5 h-1.5 w-full max-w-md overflow-hidden rounded-full bg-active"
      role="meter"
      aria-label="Disk used"
      aria-valuemin="0"
      aria-valuemax="100"
      :aria-valuenow="display.percent"
    >
      <div class="h-full rounded-full" :class="FILL[display.tone]" :style="{ width: `${display.percent}%` }" />
    </div>
    <p class="mt-1 text-xs text-fg-subtle">
      The disk that holds the agent's data, images and volumes; what <span class="mono">df</span> shows.
    </p>
  </div>
  <span v-else class="text-fg-muted">Not measured: this agent does not run on Linux.</span>
</template>
