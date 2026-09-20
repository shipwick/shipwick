<script setup lang="ts">
import type { AgentEvent } from '~/types/api'
import { formatAbsoluteUtc } from '~/utils/format'
import { eventLevelTone } from '~/utils/status'

const props = withDefaults(defineProps<{
  events: AgentEvent[]
  /** Also print the absolute UTC time next to the relative one (deployment timelines). */
  absolute?: boolean
  /** Show the event type (step / state / log / app / supervisor). */
  showType?: boolean
}>(), { absolute: false, showType: false })

const TONE_TEXT = { ok: 'text-ok', warn: 'text-warn', danger: 'text-danger', muted: 'text-fg' } as const
</script>

<template>
  <ol class="divide-y divide-line">
    <li
      v-for="event in props.events"
      :key="event.id"
      class="grid grid-cols-1 gap-x-4 gap-y-0.5 px-4 py-2 sm:grid-cols-[var(--time-col)_1fr]"
      :style="{ '--time-col': props.absolute ? '17rem' : '5.5rem' }"
    >
      <div class="flex items-baseline gap-3 text-xs text-fg-subtle">
        <TimeAgo :time="event.created_at" :class="props.absolute ? 'w-[4.5rem] shrink-0' : ''" />
        <span v-if="props.absolute" class="mono text-fg-faint">{{ formatAbsoluteUtc(event.created_at) }}</span>
      </div>
      <div class="flex min-w-0 gap-2" :class="event.type === 'log' ? 'items-start' : 'items-baseline'">
        <span v-if="props.showType" class="label w-16 shrink-0">{{ event.type }}</span>
        <span v-if="event.level !== 'info'" class="label shrink-0" :class="TONE_TEXT[eventLevelTone(event.level)]">{{ event.level }}</span>
        <pre
          v-if="event.type === 'log'"
          class="mono min-w-0 flex-1 overflow-x-auto whitespace-pre rounded-sm border border-line bg-inset px-3 py-2 text-xs leading-[1.125rem]"
        >{{ event.message }}</pre>
        <!-- The level label carries the color; the message stays in ink so a crash loop is not a wall of amber. -->
        <span v-else class="min-w-0 break-words">{{ event.message }}</span>
      </div>
    </li>
  </ol>
</template>
