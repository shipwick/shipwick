<script setup lang="ts">
import type { Deployment, LogArchiveEntry } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { isAbortError, toAgentError } from '~/utils/agentError'
import { formatAbsoluteUtc, formatBytes, pluralize } from '~/utils/format'
import { endedBadly, endedBecause, linesKept, logsPath, outputOf } from '~/utils/logArchive'

/**
 * What is kept of the containers of one application that ended, newest first.
 * The filters are in the address, so "the output of the replicas of
 * deployment 12" is a link. Read a page at a time and not polled: a list that
 * moves under the reader is hard to read.
 */
const props = defineProps<{
  application: string
  /** '' for both kinds. */
  kind: string
  /** A deployment's id; null for all. */
  deployment: number | null
  replica: number | null
  /** The application's deployments, to choose one from. */
  deployments: readonly Deployment[]
}>()

const emit = defineEmits<{
  /** A filter was changed: `null` and '' clear it. */
  filter: [changes: { kind?: string, deployment?: number | null, replica?: number | null }]
}>()

const agent = useAgent()
const server = useServerInfo()

const PAGE = 50

const entries = shallowRef<LogArchiveEntry[]>([])
const loading = ref(true)
const loadingOlder = ref(false)
const older = ref(false)
const error = shallowRef<AgentError | null>(null)
let controller: AbortController | null = null

async function load(before?: number) {
  controller?.abort()
  const own = new AbortController()
  controller = own
  if (before === undefined) loading.value = true
  else loadingOlder.value = true
  error.value = null
  try {
    const page = await agent.get<LogArchiveEntry[]>(`/applications/${encodeURIComponent(props.application)}/logs/archive`, {
      query: { kind: props.kind, deployment: props.deployment, replica: props.replica, limit: PAGE, before },
      signal: own.signal,
    })
    entries.value = before === undefined ? page : [...entries.value, ...page]
    older.value = page.length >= PAGE
  }
  catch (cause) {
    if (isAbortError(cause) || own.signal.aborted) return
    error.value = toAgentError(cause)
  }
  finally {
    if (controller === own) {
      loading.value = false
      loadingOlder.value = false
    }
  }
}

function loadOlder() {
  const last = entries.value[entries.value.length - 1]
  if (last) void load(last.id)
}

onMounted(() => void load())
watch(() => [props.application, props.kind, props.deployment, props.replica], () => void load())
onScopeDispose(() => controller?.abort())

const filtered = computed(() => props.kind !== '' || props.deployment !== null || props.replica !== null)
const archive = computed(() => server.data.value?.log_archive ?? null)

/** The deployments to choose from; one named in the address that is not among the latest is offered as well. */
const deploymentChoices = computed(() => {
  const list = props.deployments.map(d => ({ id: d.id, label: `#${d.sequence} · ${d.version}` }))
  if (props.deployment !== null && !list.some(d => d.id === props.deployment)) list.push({ id: props.deployment, label: `deployment ${props.deployment}` })
  return list
})

/** Replicas to choose from: as many as any deployment had, and the one in the address. */
const replicaChoices = computed(() => {
  const most = Math.max(1, props.replica ?? 1, ...entries.value.map(e => e.replica))
  return Array.from({ length: most }, (_, i) => i + 1)
})

const number = (value: string) => (value === '' ? null : Number(value))
</script>

<template>
  <div class="space-y-4">
    <form class="grid gap-3 sm:grid-cols-[minmax(0,12rem)_minmax(0,16rem)_minmax(0,9rem)_auto] sm:items-end" @submit.prevent="load()">
      <div>
        <label for="archive-kind" class="label block">Output of</label>
        <select id="archive-kind" class="input mt-1.5" :value="props.kind" @change="emit('filter', { kind: ($event.target as HTMLSelectElement).value })">
          <option value="">
            Replicas and runs
          </option>
          <option value="replica">
            Replicas
          </option>
          <option value="run">
            Runs of jobs and commands
          </option>
        </select>
      </div>
      <div>
        <label for="archive-deployment" class="label block">Deployment</label>
        <select id="archive-deployment" class="input mono mt-1.5" :value="props.deployment ?? ''" @change="emit('filter', { deployment: number(($event.target as HTMLSelectElement).value) })">
          <option value="">
            All
          </option>
          <option v-for="d in deploymentChoices" :key="d.id" :value="d.id">
            {{ d.label }}
          </option>
        </select>
      </div>
      <div>
        <label for="archive-replica" class="label block">Replica</label>
        <select id="archive-replica" class="input mono mt-1.5" :value="props.replica ?? ''" :disabled="props.kind === 'run'" @change="emit('filter', { replica: number(($event.target as HTMLSelectElement).value) })">
          <option value="">
            All
          </option>
          <option v-for="r in replicaChoices" :key="r" :value="r">
            {{ r }}
          </option>
        </select>
      </div>
      <div class="flex gap-2">
        <UiButton type="submit" :pending="loading && entries.length > 0">
          <UiIcon name="refresh" :size="12" />
          Refresh
        </UiButton>
        <UiButton v-if="filtered" variant="ghost" @click="emit('filter', { kind: '', deployment: null, replica: null })">
          Clear
        </UiButton>
      </div>
    </form>

    <div class="overflow-hidden rounded-sm border border-line">
      <TableSkeleton v-if="loading && entries.length === 0" :rows="6" :columns="5" />
      <ErrorState v-else-if="error && entries.length === 0" :error="error" subject="the kept output" :retrying="loading" @retry="load()" />
      <EmptyState v-else-if="entries.length === 0" :title="filtered ? 'Nothing kept matches' : 'Nothing is kept yet'">
        <template v-if="archive && !archive.enabled">
          The log archive is off on this server (<span class="mono text-fg">SHIPWICK_LOG_RETENTION_SIZE=0</span>), so nothing is kept when a container ends.
        </template>
        <template v-else-if="filtered">
          No container of that kind, deployment and replica has ended and is still kept. Kept output is removed after {{ archive ? pluralize(archive.retention_days, 'day') : 'a while' }}.
        </template>
        <template v-else>
          The output of a container is kept when it ends: a replica that crashes, is restarted, stopped or replaced, and every run of a job or a command. Nothing of {{ props.application }} has ended since this server keeps output, or what ended had written nothing.
        </template>
      </EmptyState>
      <div v-else class="overflow-x-auto">
        <table class="data-table stack" :aria-label="`Kept output of ${props.application}`">
          <thead>
            <tr>
              <th>Output of</th>
              <th>Ended</th>
              <th>How it ended</th>
              <th class="right">
                Lines
              </th>
              <th class="right">
                Size
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in entries" :key="e.id">
              <td data-primary>
                <NuxtLink :to="logsPath(props.application, { view: 'archive', entry: e.id })" class="mono link font-medium" :title="e.container">{{ outputOf(e) }}</NuxtLink>
                <span v-if="e.version" class="mono ml-2 text-xs text-fg-subtle">{{ e.version }}</span>
              </td>
              <td data-label="Ended" class="whitespace-nowrap text-fg-muted" :title="formatAbsoluteUtc(e.ended_at)">
                <TimeAgo :time="e.ended_at" />
              </td>
              <td data-label="How it ended" :class="endedBadly(e) ? 'text-danger' : 'text-fg-muted'" :title="e.reason">
                {{ endedBecause(e) }}
              </td>
              <td data-label="Lines" class="mono right whitespace-nowrap text-fg-muted" :title="e.truncated ? 'It wrote more than is kept of one container: these are its last lines' : undefined">
                {{ linesKept(e) }}
              </td>
              <td data-label="Size" class="mono right whitespace-nowrap text-fg-muted">
                {{ e.lines > 0 ? formatBytes(e.bytes) : '—' }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-if="entries.length > 0" class="flex flex-wrap items-center justify-between gap-3 border-t border-line px-4 py-2">
        <p class="text-xs text-fg-subtle">
          {{ pluralize(entries.length, 'entry', 'entries') }}{{ older ? ', newest first' : '; that is all that is kept' }}.
          <template v-if="archive?.enabled">
            Output is kept for {{ pluralize(archive.retention_days, 'day') }}.
          </template>
        </p>
        <UiButton v-if="older" size="sm" :pending="loadingOlder" @click="loadOlder">
          Load older
        </UiButton>
      </div>
      <InlineError v-if="error && entries.length > 0" :error="error" class="m-3" />
    </div>
  </div>
</template>
