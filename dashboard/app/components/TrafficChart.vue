<script setup lang="ts">
import type { TrafficRange } from '~/types/api'
import type { ChartPoint } from '~/utils/metricsHistory'
import { timeTicks } from '~/utils/metricsHistory'
import { formatAbsoluteUtc, formatLogTime } from '~/utils/format'

export interface TrafficSeries {
  key: string
  /** Names the line in the legend and the tooltip. */
  label: string
  /** A CSS color: a series or a meaning token. */
  color: string
  /** Runs of consecutive steps; a series without gaps is one run. */
  segments: ChartPoint[][]
  /** Shown next to the label in the legend: the window's total, or its percentile. */
  summary: string
}

/**
 * One plot of the traffic panel: a line per series over the window, drawn the
 * way MetricsHistoryChart draws its own — a stretched viewBox with
 * non-scaling strokes, and everything that must not be stretched (labels,
 * markers, the tooltip) as HTML placed by percentage.
 */
const props = defineProps<{
  label: string
  series: TrafficSeries[]
  start: number
  end: number
  stepMs: number
  /** The y axis spans 0..max. */
  max: number
  range: TrafficRange
  format: (value: number) => string
}>()

const WIDTH = 600
const HEIGHT = 120
const PAD = 4

const xOf = (t: number) => {
  const span = props.end - props.start
  return span <= 0 ? WIDTH : ((t - props.start) / span) * WIDTH
}
const yOf = (value: number) => HEIGHT - PAD - Math.min(1, Math.max(0, value / props.max)) * (HEIGHT - PAD * 2)

const paths = computed(() => props.series.map(s => ({
  ...s,
  d: s.segments.filter(seg => seg.length > 1).map(seg => seg.map((p, i) => `${i === 0 ? 'M' : 'L'}${xOf(p.t).toFixed(2)} ${yOf(p.value).toFixed(2)}`).join(' ')).join(' '),
  // A step with requests between two without has no line to belong to.
  singles: s.segments.filter(seg => seg.length === 1).map(seg => seg[0]!),
})))

const gridValues = computed(() => [props.max, props.max / 2])
const ticks = computed(() => timeTicks(props.start, props.end, props.range))

function tickLabel(t: number): string {
  const d = new Date(t)
  if (props.range === '7d') return `${d.getUTCDate()} ${d.toLocaleString('en-US', { month: 'short', timeZone: 'UTC' })}`
  return formatLogTime(d.toISOString()).slice(0, 5)
}

const description = computed(() => `${props.label} over the last ${props.range}: ${props.series.map(s => `${s.label} ${s.summary}`).join(', ')}`)

// Hover: the step nearest to the pointer, and each series' value in it.
const root = ref<HTMLElement | null>(null)
const hoveredT = ref<number | null>(null)

function onPointerMove(event: PointerEvent) {
  const el = root.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const t = props.start + ((event.clientX - rect.left) / rect.width) * (props.end - props.start)
  const step = Math.round((t - props.start) / props.stepMs)
  hoveredT.value = props.start + Math.max(0, step) * props.stepMs
}

interface HoverRow { key: string, label: string, color: string, point: ChartPoint }

const hovered = computed<{ t: number, rows: HoverRow[] } | null>(() => {
  const t = hoveredT.value
  if (t === null || t > props.end) return null
  const rows: HoverRow[] = []
  for (const s of props.series) {
    for (const seg of s.segments) {
      const point = seg.find(p => p.t === t)
      if (point) {
        rows.push({ key: s.key, label: s.label, color: s.color, point })
        break
      }
    }
  }
  return { t, rows }
})

const pct = (value: number, total: number) => `${(value / total) * 100}%`
</script>

<template>
  <div class="px-4 py-3">
    <div class="mb-2 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
      <span class="label">{{ props.label }}</span>
      <!-- Legend: identity by mark and name, never by color alone. -->
      <ul class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs" :aria-label="`${props.label} series`">
        <li v-for="s in props.series" :key="s.key" class="flex items-center gap-1.5 text-fg-muted">
          <span class="h-0.5 w-3 rounded-full" :style="{ background: s.color }" aria-hidden="true" />
          {{ s.label }}
          <span class="mono text-fg">{{ s.summary }}</span>
        </li>
      </ul>
    </div>

    <div
      ref="root"
      class="relative h-[7.5rem] w-full touch-none"
      role="img"
      :aria-label="description"
      @pointermove="onPointerMove"
      @pointerleave="hoveredT = null"
    >
      <svg :viewBox="`0 0 ${WIDTH} ${HEIGHT}`" preserveAspectRatio="none" class="absolute inset-0 size-full overflow-visible" aria-hidden="true">
        <line v-for="v in gridValues" :key="v" x1="0" :y1="yOf(v)" :x2="WIDTH" :y2="yOf(v)" stroke="var(--line)" stroke-width="1" vector-effect="non-scaling-stroke" />
        <line x1="0" :y1="HEIGHT - 0.5" :x2="WIDTH" :y2="HEIGHT - 0.5" stroke="var(--line-strong)" stroke-width="1" vector-effect="non-scaling-stroke" />
        <path
          v-for="s in paths"
          :key="s.key"
          :d="s.d"
          fill="none"
          :stroke="s.color"
          stroke-width="2"
          stroke-linejoin="round"
          stroke-linecap="round"
          vector-effect="non-scaling-stroke"
        />
        <line
          v-if="hovered"
          :x1="xOf(hovered.t)"
          y1="0"
          :x2="xOf(hovered.t)"
          :y2="HEIGHT"
          stroke="var(--fg-faint)"
          stroke-width="1"
          vector-effect="non-scaling-stroke"
        />
      </svg>

      <span class="mono pointer-events-none absolute right-0 top-0 -translate-y-1/2 bg-bg px-1 text-2xs text-fg-subtle">{{ props.format(props.max) }}</span>
      <span class="mono pointer-events-none absolute right-0 top-1/2 -translate-y-1/2 bg-bg px-1 text-2xs text-fg-subtle">{{ props.format(props.max / 2) }}</span>

      <template v-for="s in paths" :key="`m${s.key}`">
        <span
          v-for="p in s.singles"
          :key="p.t"
          class="pointer-events-none absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2 ring-bg"
          :style="{ left: pct(xOf(p.t), WIDTH), top: pct(yOf(p.value), HEIGHT), background: s.color }"
        />
      </template>

      <template v-if="hovered">
        <span
          v-for="row in hovered.rows"
          :key="row.key"
          class="pointer-events-none absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2 ring-bg"
          :style="{ left: pct(xOf(row.point.t), WIDTH), top: pct(yOf(row.point.value), HEIGHT), background: row.color }"
        />
        <div
          class="pointer-events-none absolute top-0 z-10 rounded-sm border border-line-strong bg-bg px-2 py-1 text-2xs"
          :class="xOf(hovered.t) > WIDTH / 2 ? '-translate-x-full -ml-2' : 'ml-2'"
          :style="{ left: pct(xOf(hovered.t), WIDTH) }"
        >
          <p class="mono whitespace-nowrap text-fg-subtle">
            {{ formatAbsoluteUtc(new Date(hovered.t).toISOString()) }}
          </p>
          <p v-if="hovered.rows.length === 0" class="text-fg-subtle">
            No requests
          </p>
          <p v-for="row in hovered.rows" :key="row.key" class="flex items-center gap-1.5 whitespace-nowrap text-fg">
            <span class="size-1.5 rounded-full" :style="{ background: row.color }" aria-hidden="true" />{{ row.label }} <span class="mono">{{ props.format(row.point.value) }}</span>
          </p>
        </div>
      </template>
    </div>

    <div class="relative mt-1 h-4" aria-hidden="true">
      <span
        v-for="t in ticks"
        :key="t"
        class="mono absolute -translate-x-1/2 whitespace-nowrap text-2xs text-fg-subtle"
        :style="{ left: pct(xOf(t), WIDTH) }"
      >{{ tickLabel(t) }}</span>
    </div>
  </div>
</template>
