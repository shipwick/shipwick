<script setup lang="ts">
import type { MetricsHistory, MetricsRange } from '~/types/api'
import type { ChartPoint, HistoryMetric } from '~/utils/metricsHistory'
import { timeTicks, toChartData } from '~/utils/metricsHistory'
import { formatAbsoluteUtc, formatLogTime } from '~/utils/format'

/**
 * One metric of the sampled history: a line per replica, a dashed line at the
 * limit, gaps where a replica had no sample. Hand-made SVG, like Sparkline:
 * the plot is a stretched viewBox with non-scaling strokes, and everything
 * that must not be stretched (labels, markers, the tooltip) is HTML placed by
 * percentage.
 */
const props = defineProps<{
  history: MetricsHistory
  metric: HistoryMetric
  range: MetricsRange
  /** "CPU", "Memory": names the chart for the legend and the description. */
  label: string
  format: (value: number) => string
}>()

const WIDTH = 600
const HEIGHT = 120
const PAD = 4

const chart = computed(() => toChartData(props.history, props.metric, props.range))

const xOf = (t: number) => {
  const span = chart.value.end - chart.value.start
  return span <= 0 ? WIDTH : ((t - chart.value.start) / span) * WIDTH
}
const yOf = (value: number) => HEIGHT - PAD - Math.min(1, Math.max(0, value / chart.value.max)) * (HEIGHT - PAD * 2)

/** Replica n always gets series color n, the same as in the log viewer. */
const colorOf = (replica: number) => `var(--series-${((Math.max(1, replica) - 1) % 8) + 1})`

const paths = computed(() => chart.value.series.map(s => ({
  replica: s.replica,
  color: colorOf(s.replica),
  // A one-point segment has no line; it is drawn as a marker below.
  d: s.segments.filter(seg => seg.length > 1).map(seg => seg.map((p, i) => `${i === 0 ? 'M' : 'L'}${xOf(p.t).toFixed(2)} ${yOf(p.value).toFixed(2)}`).join(' ')).join(' '),
  singles: s.segments.filter(seg => seg.length === 1).map(seg => seg[0]!),
  latest: s.latest,
})))

const limitY = computed(() => (chart.value.limit !== null && chart.value.limit <= chart.value.max ? yOf(chart.value.limit) : null))

const gridValues = computed(() => [chart.value.max, chart.value.max / 2])
const ticks = computed(() => timeTicks(chart.value.start, chart.value.end, props.range))

function tickLabel(t: number): string {
  const d = new Date(t)
  if (props.range === '7d') return `${d.getUTCDate()} ${d.toLocaleString('en-US', { month: 'short', timeZone: 'UTC' })}`
  return formatLogTime(d.toISOString()).slice(0, 5)
}

const empty = computed(() => chart.value.series.every(s => s.segments.length === 0))

const description = computed(() => {
  if (empty.value) return `${props.label}: no samples in this range`
  const parts = chart.value.series.map(s => `replica ${s.replica} now ${s.latest ? props.format(s.latest.value) : 'no data'}`)
  const limit = chart.value.limit !== null ? `, limit ${props.format(chart.value.limit)}` : ''
  return `${props.label} over the last ${props.range}: ${parts.join(', ')}${limit}`
})

// Hover: the bucket nearest to the pointer, and each replica's point in it.
const root = ref<HTMLElement | null>(null)
const hoveredT = ref<number | null>(null)

function onPointerMove(event: PointerEvent) {
  const el = root.value
  if (!el || empty.value) return
  const rect = el.getBoundingClientRect()
  const span = chart.value.end - chart.value.start
  const t = chart.value.start + ((event.clientX - rect.left) / rect.width) * span
  hoveredT.value = Math.round(t / chart.value.stepMs) * chart.value.stepMs
}

interface HoverRow { replica: number, color: string, point: ChartPoint }

const hovered = computed<{ t: number, rows: HoverRow[] } | null>(() => {
  const t = hoveredT.value
  if (t === null) return null
  const half = chart.value.stepMs / 2
  const rows: HoverRow[] = []
  for (const s of chart.value.series) {
    for (const seg of s.segments) {
      const point = seg.find(p => Math.abs(p.t - t) <= half)
      if (point) {
        rows.push({ replica: s.replica, color: colorOf(s.replica), point })
        break
      }
    }
  }
  return rows.length > 0 ? { t: rows[0]!.point.t, rows } : { t, rows }
})

const pct = (value: number, total: number) => `${(value / total) * 100}%`

/** Per-replica figures for the table view, the accessible alternative to the plot. */
const table = computed(() => chart.value.series.map((s) => {
  const values = s.segments.flat().map(p => p.value)
  const n = values.length
  return {
    replica: s.replica,
    color: colorOf(s.replica),
    samples: n,
    min: n ? Math.min(...values) : null,
    avg: n ? values.reduce((a, b) => a + b, 0) / n : null,
    max: n ? Math.max(...values) : null,
    latest: s.latest?.value ?? null,
  }
}))
</script>

<template>
  <div class="px-4 py-3">
    <div class="mb-2 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
      <span class="label">{{ props.label }}</span>
      <!-- Legend: identity by mark and number, never by color alone. -->
      <ul v-if="!empty" class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs" aria-label="Replicas">
        <li v-for="s in paths" :key="s.replica" class="mono flex items-center gap-1.5 text-fg-muted">
          <span class="h-0.5 w-3 rounded-full" :style="{ background: s.color }" aria-hidden="true" />
          r{{ s.replica }}
          <span class="text-fg">{{ s.latest ? props.format(s.latest.value) : '—' }}</span>
        </li>
        <li v-if="chart.limit !== null" class="flex items-center gap-1.5 text-fg-subtle">
          <span class="w-3 border-t border-dashed border-line-strong" aria-hidden="true" />
          limit {{ props.format(chart.limit) }}
        </li>
      </ul>
    </div>

    <p v-if="empty" class="flex h-[7.5rem] items-center text-xs text-fg-subtle">
      No samples in this range. The first point appears about a minute after a replica starts.
    </p>

    <div
      v-else
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
        <line
          v-if="limitY !== null"
          x1="0"
          :y1="limitY"
          :x2="WIDTH"
          :y2="limitY"
          stroke="var(--fg-faint)"
          stroke-width="1"
          stroke-dasharray="4 3"
          vector-effect="non-scaling-stroke"
        />
        <path
          v-for="s in paths"
          :key="s.replica"
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

      <!-- Y labels and markers are HTML so the stretched viewBox cannot distort them. -->
      <span class="mono pointer-events-none absolute right-0 top-0 -translate-y-1/2 bg-bg px-1 text-2xs text-fg-subtle">{{ props.format(chart.max) }}</span>
      <span class="mono pointer-events-none absolute right-0 top-1/2 -translate-y-1/2 bg-bg px-1 text-2xs text-fg-subtle">{{ props.format(chart.max / 2) }}</span>

      <template v-for="s in paths" :key="`m${s.replica}`">
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
          :key="row.replica"
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
            No sample
          </p>
          <p v-for="row in hovered.rows" :key="row.replica" class="mono flex items-center gap-1.5 whitespace-nowrap text-fg">
            <span class="size-1.5 rounded-full" :style="{ background: row.color }" aria-hidden="true" />r{{ row.replica }} {{ props.format(row.point.value) }}
          </p>
        </div>
      </template>
    </div>

    <div v-if="!empty" class="relative mt-1 h-4" aria-hidden="true">
      <span
        v-for="t in ticks"
        :key="t"
        class="mono absolute -translate-x-1/2 whitespace-nowrap text-2xs text-fg-subtle"
        :style="{ left: pct(xOf(t), WIDTH) }"
      >{{ tickLabel(t) }}</span>
    </div>

    <details v-if="!empty" class="mt-2 text-xs">
      <summary class="cursor-pointer select-none text-fg-subtle hover:text-fg">
        Show as table
      </summary>
      <table class="data-table mt-2 !text-xs">
        <thead>
          <tr>
            <th>Replica</th>
            <th class="right">
              Samples
            </th>
            <th class="right">
              Min
            </th>
            <th class="right">
              Average
            </th>
            <th class="right">
              Peak
            </th>
            <th class="right">
              Latest
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in table" :key="row.replica">
            <td class="mono">
              <span class="mr-1.5 inline-block size-1.5 rounded-full align-middle" :style="{ background: row.color }" aria-hidden="true" />r{{ row.replica }}
            </td>
            <td class="mono right">
              {{ row.samples }}
            </td>
            <td class="mono right">
              {{ row.min === null ? '—' : props.format(row.min) }}
            </td>
            <td class="mono right">
              {{ row.avg === null ? '—' : props.format(row.avg) }}
            </td>
            <td class="mono right">
              {{ row.max === null ? '—' : props.format(row.max) }}
            </td>
            <td class="mono right">
              {{ row.latest === null ? '—' : props.format(row.latest) }}
            </td>
          </tr>
        </tbody>
      </table>
    </details>
  </div>
</template>
