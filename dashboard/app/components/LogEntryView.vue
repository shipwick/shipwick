<script setup lang="ts">
import type { LogArchiveDetail, LogArchiveEntry } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { isAbortError, toAgentError } from '~/utils/agentError'
import { formatAbsoluteUtc, formatBytes, pluralize } from '~/utils/format'
import { endedBadly, endedBecause, entrySubject, linesKept, logsPath } from '~/utils/logArchive'

/**
 * The kept output of one container that ended: the entry named by `entry`,
 * or without one the replica that ended last — what `shipwick logs --previous`
 * prints. Its header says whose output it is and how it ended, before the
 * lines.
 */
const props = defineProps<{
  application: string
  /** An entry of the archive; null for the newest of a replica. */
  entry: number | null
  /** Why the page shows this without having been asked to; "" otherwise. */
  note?: string
}>()

const agent = useAgent()
const server = useServerInfo()

const detail = shallowRef<LogArchiveDetail | null>(null)
const loading = ref(true)
/** Loaded, and the archive holds nothing to show. */
const none = ref(false)
const error = shallowRef<AgentError | null>(null)
let controller: AbortController | null = null

const base = computed(() => `/applications/${encodeURIComponent(props.application)}/logs/archive`)

async function load() {
  controller?.abort()
  const own = new AbortController()
  controller = own
  loading.value = true
  error.value = null
  none.value = false
  try {
    let id = props.entry
    if (id === null) {
      const newest = await agent.get<LogArchiveEntry[]>(base.value, { query: { kind: 'replica', limit: 1 }, signal: own.signal })
      id = newest[0]?.id ?? null
    }
    if (id === null) {
      detail.value = null
      none.value = true
      return
    }
    detail.value = await agent.get<LogArchiveDetail>(`${base.value}/${id}`, { signal: own.signal })
  }
  catch (cause) {
    if (isAbortError(cause) || own.signal.aborted) return
    error.value = toAgentError(cause)
    detail.value = null
  }
  finally {
    if (controller === own) loading.value = false
  }
}

onMounted(load)
watch(() => [props.application, props.entry], load)
onScopeDispose(() => controller?.abort())

const e = computed(() => detail.value)
const archive = computed(() => server.data.value?.log_archive ?? null)
const kept = computed(() => (archive.value ? pluralize(archive.value.retention_days, 'day') : 'a while'))
/** The entry was asked for by its number and is not there: aged out, or never this application's. */
const gone = computed(() => props.entry !== null && error.value?.code === 'NOT_FOUND')
</script>

<template>
  <div class="overflow-hidden rounded-sm border border-line bg-bg">
    <div v-if="loading && !e" class="space-y-2 px-4 py-4" role="progressbar" aria-busy="true" aria-label="Loading">
      <span class="skeleton w-64" /><span class="skeleton w-96 max-w-full" /><span class="skeleton h-24 w-full" />
    </div>

    <EmptyState v-else-if="gone" title="This output is no longer kept">
      Entry {{ props.entry }} is not in the archive of {{ props.application }}: kept output is removed after {{ kept }}, earlier when the archive is full, and with its application.
      <NuxtLink :to="logsPath(props.application, { view: 'archive' })" class="link">
        See what is kept
      </NuxtLink>
    </EmptyState>

    <ErrorState v-else-if="error" :error="error" subject="the kept output" :retrying="loading" @retry="load" />

    <EmptyState v-else-if="none" title="Nothing is kept of an earlier container yet">
      <template v-if="archive && !archive.enabled">
        The log archive is off on this server (<span class="mono text-fg">SHIPWICK_LOG_RETENTION_SIZE=0</span>), so nothing is kept when a container ends.
      </template>
      <template v-else>
        When a replica crashes, is restarted, stopped or replaced, what it wrote last is kept here for {{ kept }}. Nothing of {{ props.application }} has ended since this server keeps output, or what ended had written nothing.
      </template>
    </EmptyState>

    <template v-else-if="e">
      <div class="border-b border-line px-4 py-3">
        <p v-if="props.note" class="mb-2 flex items-start gap-2 text-xs text-fg-muted">
          <UiIcon name="alert" :size="14" class="mt-px text-fg-subtle" />
          <span class="min-w-0">{{ props.note }}</span>
        </p>
        <p class="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span class="font-medium">{{ entrySubject(e) }}</span>
          <StatusBadge :tone="endedBadly(e) ? 'danger' : 'muted'" :label="endedBecause(e)" :raw="e.reason" />
        </p>
        <p class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-fg-muted">
          <span>ended <TimeAgo :time="e.ended_at" /> <span class="mono text-fg-subtle">{{ formatAbsoluteUtc(e.ended_at) }}</span></span>
          <span class="text-fg-faint" aria-hidden="true">·</span>
          <span>{{ linesKept(e) }}<template v-if="e.lines > 0">, {{ formatBytes(e.bytes) }}</template></span>
          <template v-if="e.deployment_id !== null">
            <span class="text-fg-faint" aria-hidden="true">·</span>
            <NuxtLink :to="`/deployments/${e.deployment_id}`" class="link">deployment #{{ e.deployment }}</NuxtLink>
          </template>
          <span class="text-fg-faint" aria-hidden="true">·</span>
          <span class="mono break-all">{{ e.container }}</span>
        </p>
        <p v-if="e.truncated" class="mt-1 text-xs text-fg-subtle">
          It wrote more than is kept of one container: these are its last lines.
        </p>
      </div>
      <LogOutput
        :lines="e.output"
        :label="`Output of ${entrySubject(e)}`"
        height-class="max-h-[calc(100dvh-20rem)] min-h-40"
        :empty="e.kind === 'run' ? 'The command wrote nothing.' : 'This container ended without writing anything.'"
      />
    </template>
  </div>
</template>
