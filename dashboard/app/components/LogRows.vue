<script setup lang="ts">
import type { LogLine } from '~/types/api'
import { formatLogTime } from '~/utils/format'

/**
 * Lines of container output, one row each: time, replica, the line itself.
 * The rows of every log on the dashboard — followed, kept or found — so that
 * they read alike. It is not a region of its own: whatever holds it is the
 * `role="log"`.
 */
export interface LogRow extends LogLine {
  /** Unique within its list; the position when absent. */
  seq?: number
  /** A message of the viewer itself, not a line of a container. */
  notice?: boolean
}

const props = withDefaults(defineProps<{
  lines: readonly LogRow[]
  timestamps?: boolean
  wrap?: boolean
  /** The replica mark before each line; off for the output of a single container. */
  replicas?: boolean
  /** A text whose occurrences in the lines are marked, whatever its case. */
  highlight?: string
}>(), { timestamps: true, wrap: false, replicas: true, highlight: '' })

/** Replica n always gets series color n: color follows the entity, and the number is always printed too. */
function replicaColor(replica: number): string {
  return `var(--series-${((Math.max(1, replica) - 1) % 8) + 1})`
}

/** A line cut at every occurrence of the marked text: [before, match, between, match, …]. */
function pieces(message: string): string[] {
  const needle = props.highlight.toLowerCase()
  if (needle === '') return [message]
  const out: string[] = []
  const lower = message.toLowerCase()
  let from = 0
  for (let at = lower.indexOf(needle); at !== -1; at = lower.indexOf(needle, from)) {
    out.push(message.slice(from, at), message.slice(at, at + needle.length))
    from = at + needle.length
  }
  out.push(message.slice(from))
  return out
}
</script>

<template>
  <div :class="props.wrap ? '' : 'w-max min-w-full'">
    <template v-for="(line, index) in props.lines" :key="line.seq ?? index">
      <div v-if="line.notice" class="log-row my-1 border-y border-line px-3 py-1 font-sans text-fg-subtle">
        {{ line.message }}
      </div>
      <div v-else class="log-row flex items-start gap-3 px-3 hover:bg-hover">
        <span v-if="props.timestamps" class="shrink-0 select-none text-fg-subtle" :title="line.time">{{ formatLogTime(line.time) }}</span>
        <span v-if="props.replicas" class="flex w-7 shrink-0 select-none items-center gap-1 text-fg-subtle" :title="line.container">
          <span class="h-3 w-0.5 rounded-full" :style="{ background: replicaColor(line.replica) }" aria-hidden="true" />r{{ line.replica }}
        </span>
        <span class="min-w-0" :class="props.wrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'"><span v-if="line.stream === 'stderr'" class="mr-2 select-none text-2xs uppercase tracking-wider text-fg-subtle">err</span><template v-if="props.highlight === ''">{{ line.message }}</template><template v-for="(piece, n) in pieces(line.message)" v-else :key="n"><mark v-if="n % 2 === 1" class="rounded-xs bg-warn-bg text-fg">{{ piece }}</mark><template v-else>{{ piece }}</template></template></span>
      </div>
    </template>
  </div>
</template>

<style scoped>
/* Let the browser skip layout and paint for rows far outside the viewport: thousands of rows stay cheap. */
.log-row {
  content-visibility: auto;
  contain-intrinsic-size: auto 1.125rem;
}
</style>
