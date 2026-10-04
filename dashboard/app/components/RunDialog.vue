<script setup lang="ts">
import type { LogArchiveDetail, LogArchiveEntry, RunDetail } from '~/types/api'
import { durationBetween, formatAbsoluteUtc, formatDuration } from '~/utils/format'
import { archiveSupported, linesKept } from '~/utils/logArchive'
import { describeRunOutcome, runStatusDisplay, runTitle } from '~/utils/jobs'
import { formatArgv } from '~/utils/spec'

/**
 * One run with its output. A running one is polled every second until
 * `finished_at` is set, the same rule as for deployments: the status alone
 * does not end it.
 */
const props = defineProps<{
  open: boolean
  application: string
  runId: number | null
}>()

const emit = defineEmits<{ close: [] }>()

const agent = useAgent()
const now = useNow()

const run = usePolling<RunDetail>(
  signal => agent.get<RunDetail>(`/applications/${encodeURIComponent(props.application)}/runs/${props.runId}`, { signal }),
  { interval: 1000, enabled: () => props.open && props.runId !== null, until: r => r.finished_at !== null },
)

watch(() => props.runId, () => void run.reset())

const r = computed(() => run.data.value)
const status = computed(() => (r.value ? runStatusDisplay(r.value.status) : null))

const duration = computed(() => {
  if (!r.value) return '—'
  if (r.value.finished_at) return formatDuration(durationBetween(r.value.started_at, r.value.finished_at))
  return formatDuration(Math.max(0, Math.floor((now.value - Date.parse(r.value.started_at)) / 1000) * 1000))
})

const title = computed(() => (r.value ? `Run #${r.value.id}: ${runTitle(r.value)}` : `Run #${props.runId ?? ''}`))

// The run's record keeps the last 200 lines; from 0.7 on the agent keeps the
// whole output of a run that ended, up to ten thousand lines. Shown in its
// place when there is such an entry; the record's own tail otherwise.
const server = useServerInfo()
const kept = shallowRef<LogArchiveDetail | null>(null)
let keptFor: number | null = null
watch(() => (props.open && r.value?.finished_at ? r.value.id : null), async (id) => {
  if (id === null || id === keptFor) return
  keptFor = id
  kept.value = null
  if (!archiveSupported(server.data.value)) return
  try {
    const base = `/applications/${encodeURIComponent(props.application)}/logs/archive`
    const [entry] = await agent.get<LogArchiveEntry[]>(base, { query: { run: id, limit: 1 } })
    if (!entry || entry.lines === 0 || keptFor !== id) return
    const detail = await agent.get<LogArchiveDetail>(`${base}/${entry.id}`)
    if (keptFor === id) kept.value = detail
  }
  catch {
    // The record's own tail is shown instead.
  }
})
watch(() => props.runId, () => {
  keptFor = null
  kept.value = null
})

// A run that ends while it is watched is said: the badge changes without a sound.
const { announce } = useAnnounce()
watch(r, (next, before) => {
  if (next && before && next.id === before.id && before.finished_at === null && next.finished_at !== null) announce(`Run ${next.id}: ${describeRunOutcome(next)}`)
})
</script>

<template>
  <UiDialog :open="props.open" :title="title" size="md" @close="emit('close')">
    <div v-if="run.loading.value && !r" class="space-y-3" role="progressbar" aria-busy="true" aria-label="Loading">
      <span class="skeleton w-40" />
      <span class="skeleton h-24 w-full" />
    </div>
    <ErrorState v-else-if="!r && run.error.value" :error="run.error.value" subject="the run" :retrying="run.refreshing.value" class="!px-0 !py-2" @retry="run.refresh()" />
    <div v-else-if="r && status" class="space-y-4">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
        <StatusBadge :tone="status.tone" :label="describeRunOutcome(r)" :raw="r.status" size="md" />
        <span class="text-fg-muted">{{ r.kind === 'hook' ? 'pre-deploy hook' : r.kind === 'scheduled' ? 'started by the schedule' : 'started by hand' }}</span>
        <span v-if="!r.finished_at" class="mono text-xs text-fg-subtle">{{ duration }}</span>
      </div>

      <dl class="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-sm">
        <dt class="label pt-0.5">
          Command
        </dt>
        <dd class="mono break-all">
          {{ formatArgv(r.command) }}
        </dd>
        <dt class="label pt-0.5">
          Started
        </dt>
        <dd>
          <TimeAgo :time="r.started_at" /><span class="mono ml-2 text-xs text-fg-subtle">{{ formatAbsoluteUtc(r.started_at) }}</span>
        </dd>
        <dt class="label pt-0.5">
          {{ r.finished_at ? 'Duration' : 'Running for' }}
        </dt>
        <dd class="mono">
          {{ duration }}<span v-if="r.exit_code !== null" class="font-sans text-fg-muted"> · exit code <span class="mono text-fg">{{ r.exit_code }}</span></span>
        </dd>
        <template v-if="r.deployment_id !== null">
          <dt class="label pt-0.5">
            Deployment
          </dt>
          <dd>
            <NuxtLink :to="`/deployments/${r.deployment_id}`" class="mono link">id {{ r.deployment_id }}</NuxtLink>
            <span class="text-fg-muted"> · its image and environment were used</span>
          </dd>
        </template>
      </dl>

      <div v-if="kept && kept.run_id === r.id">
        <p class="label mb-1.5">
          Output <span class="normal-case tracking-normal text-fg-subtle">{{ kept.truncated ? linesKept(kept) : `all ${linesKept(kept)}` }}, kept by the server</span>
        </p>
        <div class="overflow-hidden rounded-sm border border-line">
          <LogOutput :lines="kept.output" :label="`Output of run ${r.id}`" height-class="max-h-80" />
        </div>
      </div>
      <div v-else>
        <p class="label mb-1.5">
          Output <span class="normal-case tracking-normal text-fg-subtle">last 200 lines</span>
        </p>
        <pre
          v-if="r.output"
          class="mono max-h-80 overflow-auto whitespace-pre rounded-sm border border-line bg-inset px-3 py-2 text-xs leading-[1.125rem]"
          tabindex="0"
        >{{ r.output }}</pre>
        <p v-else class="rounded-sm border border-line bg-inset px-3 py-2 text-xs text-fg-subtle">
          {{ r.finished_at ? 'The command wrote nothing.' : 'Nothing yet. The output arrives when the run is over.' }}
        </p>
      </div>
    </div>
    <template #footer>
      <UiButton @click="emit('close')">
        Close
      </UiButton>
    </template>
  </UiDialog>
</template>
