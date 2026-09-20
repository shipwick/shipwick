<script setup lang="ts">
import { LOG_BUFFER_LINES } from '~/composables/useLogStream'
import { formatLogTime } from '~/utils/format'

/**
 * Log tail/follow for one application. Used full-height on the Logs page and
 * as a panel on the application page.
 */
const props = withDefaults(defineProps<{
  application: string
  /** Tailwind height classes of the scrolling area. */
  heightClass?: string
  initialTail?: number
}>(), { heightClass: 'h-[28rem]', initialTail: 100 })

const TAIL_OPTIONS = [50, 100, 500, 1000, 5000]

const stream = useLogStream()
const { lines, status, error, dropped, reconnecting } = stream

const tail = ref(props.initialTail)
const follow = ref(true)
const timestamps = ref(true)
const wrap = ref(false)
const filter = ref('')
const hiddenReplicas = ref<Set<number>>(new Set())

// --- connection -------------------------------------------------------------

function connect() {
  if (!props.application) return
  void stream.connect({ application: props.application, tail: tail.value, follow: follow.value })
}

watch(() => [props.application, tail.value, follow.value] as const, () => {
  hiddenReplicas.value = new Set()
  if (props.application) connect()
  else stream.disconnect()
})

onMounted(connect)

// --- filtering --------------------------------------------------------------

const replicas = computed(() => {
  const seen = new Set<number>()
  for (const line of lines.value) if (!line.notice) seen.add(line.replica)
  return [...seen].sort((a, b) => a - b)
})

const visible = computed(() => {
  const needle = filter.value.trim().toLowerCase()
  const hidden = hiddenReplicas.value
  if (needle === '' && hidden.size === 0) return lines.value
  return lines.value.filter((line) => {
    if (line.notice) return true
    if (hidden.has(line.replica)) return false
    return needle === '' || line.message.toLowerCase().includes(needle)
  })
})

function toggleReplica(replica: number) {
  const next = new Set(hiddenReplicas.value)
  if (next.has(replica)) next.delete(replica)
  else next.add(replica)
  hiddenReplicas.value = next
}

/** Replica n always gets series color n: color follows the entity, and the number is always printed too. */
function replicaColor(replica: number): string {
  return `var(--series-${((Math.max(1, replica) - 1) % 8) + 1})`
}

// --- autoscroll -------------------------------------------------------------

const scroller = ref<HTMLElement | null>(null)
/** True while the view is pinned to the newest line. Scrolling up unpins it. */
const pinned = ref(true)
const unseen = ref(0)

function isAtBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < 24
}

function scrollToBottom() {
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
}

function onScroll() {
  const el = scroller.value
  if (!el) return
  const atBottom = isAtBottom(el)
  if (atBottom) unseen.value = 0
  pinned.value = atBottom
}

function resume() {
  pinned.value = true
  unseen.value = 0
  scrollToBottom()
}

function pause() {
  pinned.value = false
}

watch(visible, (next, previous) => {
  if (pinned.value) {
    void nextTick(scrollToBottom)
  }
  else {
    const lastSeen = previous[previous.length - 1]?.seq ?? 0
    unseen.value += next.filter(l => l.seq > lastSeen && !l.notice).length
  }
}, { flush: 'post' })

function clear() {
  stream.clear()
  unseen.value = 0
}

// --- status line ------------------------------------------------------------

const statusText = computed(() => {
  if (reconnecting.value) return 'Stream ended. Reconnecting in 2s'
  switch (status.value) {
    case 'connecting': return 'Connecting'
    case 'streaming': return pinned.value ? 'Following' : 'Following, autoscroll paused'
    case 'loaded': return `Last ${tail.value} lines, not following`
    case 'ended': return 'Stream ended'
    case 'error': return 'Disconnected'
    default: return 'Idle'
  }
})

const statusTone = computed(() => {
  if (status.value === 'streaming') return 'bg-ok-dot'
  if (status.value === 'error') return 'bg-danger-dot'
  if (status.value === 'connecting' || reconnecting.value) return 'bg-warn-dot'
  return 'bg-muted-dot'
})

const canReconnect = computed(() => (status.value === 'ended' && !reconnecting.value) || status.value === 'error')
</script>

<template>
  <div class="overflow-hidden rounded-sm border border-line bg-bg">
    <!-- Controls -->
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-line bg-subtle px-3 py-2">
      <slot name="leading" />

      <!-- The agent's semantics: when following, `tail` is per replica; otherwise it is the merged total. -->
      <label
        class="flex items-center gap-1.5 text-xs text-fg-muted"
        :title="follow ? 'Each replica starts with its own last N lines' : 'The last N lines across all replicas, merged by time'"
      >
        Tail
        <select v-model.number="tail" class="input mono !h-7 !w-auto !text-xs">
          <option v-for="n in TAIL_OPTIONS" :key="n" :value="n">{{ n }}</option>
        </select>
        <span v-if="follow" class="text-fg-subtle">per replica</span>
      </label>

      <label class="relative flex items-center">
        <span class="sr-only">Filter lines</span>
        <UiIcon name="search" :size="14" class="pointer-events-none absolute left-2 text-fg-subtle" />
        <input
          v-model="filter"
          type="search"
          placeholder="Filter"
          class="input mono !h-7 w-36 !pl-7 !text-xs sm:w-48"
          spellcheck="false"
          autocomplete="off"
        >
      </label>

      <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-fg-muted">
        <label class="flex cursor-pointer items-center gap-1.5"><input v-model="follow" type="checkbox" class="accent-[var(--fg)]"> Follow</label>
        <label class="flex cursor-pointer items-center gap-1.5"><input v-model="timestamps" type="checkbox" class="accent-[var(--fg)]"> Timestamps</label>
        <label class="flex cursor-pointer items-center gap-1.5"><input v-model="wrap" type="checkbox" class="accent-[var(--fg)]"> Wrap</label>
      </div>

      <div class="ml-auto flex items-center gap-1.5">
        <UiButton v-if="status === 'streaming' && pinned" size="sm" @click="pause">
          <UiIcon name="pause" :size="12" />
          Pause
        </UiButton>
        <UiButton v-else-if="!pinned" size="sm" @click="resume">
          <UiIcon name="arrow-down" :size="12" />
          Resume<span v-if="unseen > 0" class="mono text-fg-subtle">+{{ unseen > 999 ? '999' : unseen }}</span>
        </UiButton>
        <UiButton size="sm" variant="ghost" @click="clear">
          Clear
        </UiButton>
      </div>
    </div>

    <!-- Replica filter -->
    <div v-if="replicas.length > 1" class="flex flex-wrap items-center gap-1.5 border-b border-line px-3 py-1.5" role="group" aria-label="Replicas">
      <span class="label mr-1">Replicas</span>
      <button
        v-for="replica in replicas"
        :key="replica"
        type="button"
        class="mono inline-flex h-6 items-center gap-1.5 rounded-sm border px-1.5 text-xs transition-colors duration-100"
        :class="hiddenReplicas.has(replica) ? 'border-line text-fg-faint line-through' : 'border-line-strong text-fg hover:bg-hover'"
        :aria-pressed="!hiddenReplicas.has(replica)"
        :title="hiddenReplicas.has(replica) ? `Show replica ${replica}` : `Hide replica ${replica}`"
        @click="toggleReplica(replica)"
      >
        <span class="size-2 rounded-xs" :style="{ background: replicaColor(replica), opacity: hiddenReplicas.has(replica) ? 0.35 : 1 }" aria-hidden="true" />
        r{{ replica }}
      </button>
    </div>

    <!-- Lines -->
    <div
      ref="scroller"
      class="mono overflow-auto bg-inset py-1.5 text-xs leading-[1.125rem]"
      :class="props.heightClass"
      role="log"
      aria-live="off"
      :aria-label="`Logs of ${props.application}`"
      tabindex="0"
      @scroll.passive="onScroll"
    >
      <div v-if="status === 'error' && error && lines.length === 0" class="px-3 py-2 font-sans">
        <p class="font-medium text-danger">
          {{ error.unreachable ? 'Agent unreachable' : 'Could not load logs' }}
        </p>
        <p class="mt-0.5 text-fg-muted">
          {{ error.message }}
        </p>
      </div>
      <p v-else-if="status === 'connecting' && lines.length === 0" class="px-3 py-2 font-sans text-fg-subtle">
        Connecting…
      </p>
      <p v-else-if="lines.length === 0" class="px-3 py-2 font-sans text-fg-subtle">
        {{ status === 'streaming' ? 'No output yet. Waiting for new lines…' : 'No log lines.' }}
      </p>
      <p v-else-if="visible.length === 0" class="px-3 py-2 font-sans text-fg-subtle">
        No lines match the current filter.
      </p>

      <p v-if="dropped > 0 && visible.length > 0" class="px-3 pb-1 font-sans text-2xs text-fg-subtle">
        {{ dropped.toLocaleString('en-US') }} older lines dropped (buffer holds {{ LOG_BUFFER_LINES.toLocaleString('en-US') }}).
      </p>

      <div :class="wrap ? '' : 'w-max min-w-full'">
        <template v-for="line in visible" :key="line.seq">
          <div v-if="line.notice" class="log-row my-1 border-y border-line px-3 py-1 font-sans text-fg-subtle">
            {{ line.message }}
          </div>
          <div v-else class="log-row flex items-start gap-3 px-3 hover:bg-hover">
            <span v-if="timestamps" class="shrink-0 select-none text-fg-subtle" :title="line.time">{{ formatLogTime(line.time) }}</span>
            <span class="flex w-7 shrink-0 select-none items-center gap-1 text-fg-subtle" :title="line.container">
              <span class="h-3 w-0.5 rounded-full" :style="{ background: replicaColor(line.replica) }" aria-hidden="true" />r{{ line.replica }}
            </span>
            <span class="min-w-0" :class="wrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'"><span v-if="line.stream === 'stderr'" class="mr-2 select-none text-2xs uppercase tracking-wider text-fg-subtle">err</span>{{ line.message }}</span>
          </div>
        </template>
      </div>
    </div>

    <!-- Status -->
    <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-t border-line bg-subtle px-3 py-1.5 text-xs text-fg-muted">
      <span class="flex items-center gap-1.5" role="status">
        <span class="size-1.5 rounded-full" :class="statusTone" aria-hidden="true" />
        {{ statusText }}
        <span v-if="status === 'error' && error && lines.length > 0" class="text-danger">: {{ error.message }}</span>
      </span>
      <span class="flex items-center gap-3">
        <span class="mono">{{ visible.length.toLocaleString('en-US') }}<template v-if="visible.length !== lines.length"> / {{ lines.length.toLocaleString('en-US') }}</template> lines</span>
        <UiButton v-if="canReconnect" size="sm" @click="stream.reconnect()">
          <UiIcon name="refresh" :size="12" />
          Reconnect
        </UiButton>
      </span>
    </div>
  </div>
</template>

<style scoped>
/* Let the browser skip layout and paint for rows far outside the viewport: 5,000 rows stay cheap. */
.log-row {
  content-visibility: auto;
  contain-intrinsic-size: auto 1.125rem;
}
</style>
