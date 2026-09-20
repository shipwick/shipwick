<script setup lang="ts">
import { formatLogTime } from '~/utils/format'

/**
 * A small hand-made line chart of the last N samples. One series, one axis,
 * neutral ink. The newest sample sits at the right edge; the chart fills from
 * the right as history builds up, so points never shift scale horizontally.
 */
const props = withDefaults(defineProps<{
  /** What is being measured, for the accessible description: "CPU". */
  label: string
  values: number[]
  /** ISO timestamps, parallel to `values`. */
  times: string[]
  /** Fixed number of horizontal slots (the history length). */
  slots?: number
  /** A hard ceiling (a resource limit). The chart then spans 0..ceiling and draws it as a dashed line. */
  ceiling?: number | null
  format: (value: number) => string
}>(), { slots: 60, ceiling: null })

const WIDTH = 240
const HEIGHT = 40
const PAD = 3

const scaleMax = computed(() => {
  if (props.ceiling && props.ceiling > 0) return props.ceiling
  const peak = Math.max(0, ...props.values)
  // Headroom, and a floor so a flat idle series does not look like a spike.
  return peak <= 0 ? 1 : peak * 1.25
})

interface Point { x: number, y: number, value: number, time: string }

const points = computed<Point[]>(() => {
  const n = Math.min(props.values.length, props.slots)
  const values = props.values.slice(-n)
  const times = props.times.slice(-n)
  const step = WIDTH / (props.slots - 1)
  return values.map((value, i) => {
    const ratio = Math.min(1, Math.max(0, value / scaleMax.value))
    return {
      x: (props.slots - n + i) * step,
      y: HEIGHT - PAD - ratio * (HEIGHT - PAD * 2),
      value,
      time: times[i] ?? '',
    }
  })
})

const linePath = computed(() => points.value.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(2)} ${p.y.toFixed(2)}`).join(' '))

const areaPath = computed(() => {
  const first = points.value[0]
  const last = points.value[points.value.length - 1]
  if (!first || !last || points.value.length < 2) return ''
  return `${linePath.value} L${last.x.toFixed(2)} ${HEIGHT} L${first.x.toFixed(2)} ${HEIGHT} Z`
})

const last = computed(() => points.value[points.value.length - 1] ?? null)

const description = computed(() => {
  if (props.values.length === 0) return `${props.label}: no samples yet`
  const current = props.values[props.values.length - 1] ?? 0
  return `${props.label} over the last ${props.values.length} samples: now ${props.format(current)}, low ${props.format(Math.min(...props.values))}, high ${props.format(Math.max(...props.values))}`
})

// Hover layer: crosshair + readout of the nearest sample.
const root = ref<HTMLElement | null>(null)
const hovered = ref<Point | null>(null)

function onPointerMove(event: PointerEvent) {
  const el = root.value
  if (!el || points.value.length === 0) return
  const rect = el.getBoundingClientRect()
  const x = ((event.clientX - rect.left) / rect.width) * WIDTH
  let nearest = points.value[0] ?? null
  for (const p of points.value) {
    if (nearest && Math.abs(p.x - x) < Math.abs(nearest.x - x)) nearest = p
  }
  hovered.value = nearest
}

const pct = (value: number, total: number) => `${(value / total) * 100}%`
</script>

<template>
  <div
    ref="root"
    class="relative h-10 w-full touch-none text-fg-muted"
    role="img"
    :aria-label="description"
    @pointermove="onPointerMove"
    @pointerleave="hovered = null"
  >
    <svg :viewBox="`0 0 ${WIDTH} ${HEIGHT}`" preserveAspectRatio="none" class="absolute inset-0 size-full overflow-visible" aria-hidden="true">
      <line x1="0" :y1="HEIGHT - 0.5" :x2="WIDTH" :y2="HEIGHT - 0.5" stroke="var(--line)" stroke-width="1" vector-effect="non-scaling-stroke" />
      <line
        v-if="props.ceiling"
        x1="0"
        :y1="PAD"
        :x2="WIDTH"
        :y2="PAD"
        stroke="var(--line-strong)"
        stroke-width="1"
        stroke-dasharray="3 3"
        vector-effect="non-scaling-stroke"
      />
      <path v-if="areaPath" :d="areaPath" fill="currentColor" opacity="0.08" />
      <path
        v-if="points.length > 1"
        :d="linePath"
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
        stroke-linejoin="round"
        stroke-linecap="round"
        vector-effect="non-scaling-stroke"
      />
      <line
        v-if="hovered"
        :x1="hovered.x"
        y1="0"
        :x2="hovered.x"
        :y2="HEIGHT"
        stroke="var(--fg-faint)"
        stroke-width="1"
        vector-effect="non-scaling-stroke"
      />
    </svg>

    <!-- Markers are HTML so the stretched viewBox cannot squash them into ellipses. -->
    <span
      v-if="last && !hovered"
      class="pointer-events-none absolute size-[7px] -translate-x-1/2 -translate-y-1/2 rounded-full bg-fg ring-2 ring-bg"
      :style="{ left: pct(last.x, WIDTH), top: pct(last.y, HEIGHT) }"
    />
    <template v-if="hovered">
      <span
        class="pointer-events-none absolute size-[7px] -translate-x-1/2 -translate-y-1/2 rounded-full bg-fg ring-2 ring-bg"
        :style="{ left: pct(hovered.x, WIDTH), top: pct(hovered.y, HEIGHT) }"
      />
      <span
        class="mono pointer-events-none absolute bottom-full z-10 mb-1 whitespace-nowrap rounded-sm border border-line-strong bg-bg px-1.5 py-0.5 text-2xs text-fg"
        :class="hovered.x > WIDTH / 2 ? '-translate-x-full' : ''"
        :style="{ left: pct(hovered.x, WIDTH) }"
      >
        {{ props.format(hovered.value) }}
        <span class="text-fg-subtle">{{ formatLogTime(hovered.time).slice(0, 8) }} UTC</span>
      </span>
    </template>
  </div>
</template>
