<script setup lang="ts">
import type { LogLine } from '~/types/api'

/**
 * Output that is complete: a kept entry, a run. The same rows as the live
 * viewer, with its filter and its two switches, and nothing that follows.
 */
const props = withDefaults(defineProps<{
  lines: readonly LogLine[]
  /** What the region is called: "Output of replica 2 of #3". */
  label: string
  /** Tailwind height classes of the scrolling area. */
  heightClass?: string
  /** Replica marks: off for the output of one container. */
  replicas?: boolean
  /** Said in place of the lines when there are none. */
  empty?: string
}>(), { heightClass: 'max-h-[32rem]', replicas: false, empty: 'It wrote nothing.' })

const timestamps = ref(true)
const wrap = ref(false)
const filter = ref('')
const id = useId()

const visible = computed(() => {
  const needle = filter.value.trim().toLowerCase()
  return needle === '' ? props.lines : props.lines.filter(line => line.message.toLowerCase().includes(needle))
})
</script>

<template>
  <div>
    <div v-if="props.lines.length > 0" class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-line bg-subtle px-3 py-2">
      <label class="relative flex items-center">
        <span class="sr-only">Filter lines</span>
        <UiIcon name="search" :size="14" class="pointer-events-none absolute left-2 text-fg-subtle" />
        <input v-model="filter" type="search" placeholder="Filter" class="input mono !h-7 w-36 !pl-7 !text-xs sm:w-48" spellcheck="false" autocomplete="off">
      </label>
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-fg-muted">
        <label class="target flex cursor-pointer items-center gap-1.5"><input v-model="timestamps" type="checkbox" class="accent-[var(--fg)]"> Timestamps</label>
        <label class="target flex cursor-pointer items-center gap-1.5"><input v-model="wrap" type="checkbox" class="accent-[var(--fg)]"> Wrap</label>
      </div>
      <span class="mono ml-auto text-xs text-fg-muted">{{ visible.length.toLocaleString('en-US') }}<template v-if="visible.length !== props.lines.length"> / {{ props.lines.length.toLocaleString('en-US') }}</template> {{ props.lines.length === 1 ? 'line' : 'lines' }}</span>
    </div>
    <div
      :id="id"
      class="mono overflow-auto bg-inset py-1.5 text-xs leading-[1.125rem] focus-visible:-outline-offset-2"
      :class="props.heightClass"
      role="log"
      aria-live="off"
      :aria-label="props.label"
      tabindex="0"
    >
      <p v-if="props.lines.length === 0" class="px-3 py-2 font-sans text-fg-subtle">
        {{ props.empty }}
      </p>
      <p v-else-if="visible.length === 0" class="px-3 py-2 font-sans text-fg-subtle">
        No lines match the current filter.
      </p>
      <LogRows v-else :lines="visible" :timestamps="timestamps" :wrap="wrap" :replicas="props.replicas" :highlight="filter.trim()" />
    </div>
  </div>
</template>
