<script setup lang="ts">
import type { AppSpec, Job, Run, RunDetail } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { durationBetween, formatDuration } from '~/utils/format'
import { describeRunOutcome, formatNextRun, runStatusDisplay, runTitle, tidyDuration } from '~/utils/jobs'
import { roleHint } from '~/utils/roles'
import { formatArgv } from '~/utils/spec'

/**
 * The scheduled jobs of the active deployment, their run history (hook,
 * scheduled and one-off runs alike), and the two ways to start a run by hand.
 */
const props = defineProps<{
  application: string
  spec: AppSpec
  image: string
  /** The signed-in token may start runs (deploy role). */
  mayDeploy: boolean
  /** A deployment holds the application: the agent would answer 409. */
  busy: boolean
}>()

const agent = useAgent()
const now = useNow()
const path = computed(() => `/applications/${encodeURIComponent(props.application)}`)

const jobs = usePolling<Job[]>(signal => agent.get<Job[]>(`${path.value}/jobs`, { signal }), { interval: 15_000 })

// The history: one job, the hook, one-off commands, or everything. Faster while something runs.
const filter = ref('')
const anyRunning = ref(false)
const runs = usePolling<Run[]>(signal => agent.get<Run[]>(`${path.value}/runs`, { query: { job: filter.value, limit: 25 }, signal }), {
  interval: () => (anyRunning.value ? 3000 : 15_000),
})
watch(runs.data, (list) => {
  anyRunning.value = (list ?? []).some(r => r.status === 'running')
})
watch(filter, () => void runs.reset())
watch(() => props.application, () => {
  void jobs.reset()
  void runs.reset()
})

const filterOptions = computed(() => {
  const names = new Set<string>((props.spec.jobs ?? []).map(j => j.name))
  for (const r of runs.data.value ?? []) names.add(r.job)
  return [...names].sort((a, b) => a.localeCompare(b))
})

// --- starting runs ----------------------------------------------------------------

const actionError = shallowRef<AgentError | null>(null)
const startingJob = ref<string | null>(null)
const openRunId = ref<number | null>(null)
const commandOpen = ref(false)

function refresh() {
  void jobs.refresh()
  void runs.refresh()
}

async function runNow(job: Job) {
  if (startingJob.value) return
  startingJob.value = job.name
  actionError.value = null
  try {
    const run = await agent.post<RunDetail>(`${path.value}/jobs/${encodeURIComponent(job.name)}/run`)
    openRunId.value = run.id
    refresh()
  }
  catch (cause) {
    actionError.value = toAgentError(cause)
  }
  finally {
    startingJob.value = null
  }
}

function onCommandStarted(run: RunDetail) {
  openRunId.value = run.id
  refresh()
}

function closeRun() {
  openRunId.value = null
  refresh()
}

// --- presentation -------------------------------------------------------------------

const { deployHint } = useApplication()
const blocked = computed(() => deployHint.value ?? roleHint('deploy'))

const runTitleFor = (job: Job) => (!props.mayDeploy ? blocked.value : props.busy ? 'A deployment is in progress' : job.last_run?.status === 'running' ? 'A run of this job is still going' : undefined)

function runDuration(r: Run): string {
  if (r.finished_at) return formatDuration(durationBetween(r.started_at, r.finished_at))
  return formatDuration(Math.max(0, Math.floor((now.value - Date.parse(r.started_at)) / 1000) * 1000))
}

const KIND_LABEL: Record<Run['kind'], string> = { hook: 'hook', scheduled: 'schedule', manual: 'by hand' }

function openRow(r: Run, event: MouseEvent) {
  if ((event.target as HTMLElement).closest('a, button') || window.getSelection()?.toString()) return
  openRunId.value = r.id
}
</script>

<template>
  <UiPanel title="Jobs" :meta="props.spec.jobs?.length ?? 0">
    <template #actions>
      <UiButton size="sm" :disabled="!props.mayDeploy || props.busy" :title="!props.mayDeploy ? blocked : props.busy ? 'A deployment is in progress' : undefined" @click="commandOpen = true">
        <UiIcon name="play" :size="12" />
        Run command
      </UiButton>
    </template>

    <div class="divide-y divide-line">
      <InlineError :error="actionError" class="m-3" />

      <!-- Scheduled jobs -->
      <TableSkeleton v-if="jobs.loading.value && !jobs.data.value" :rows="2" :columns="5" />
      <ErrorState
        v-else-if="jobs.error.value && !jobs.data.value"
        :error="jobs.error.value"
        subject="the jobs"
        :retrying="jobs.refreshing.value"
        @retry="jobs.refresh()"
      />
      <EmptyState v-else-if="(jobs.data.value?.length ?? 0) === 0" title="No scheduled jobs">
        Add a <span class="mono text-fg">jobs</span> list to deploy.yaml to run commands on a schedule. One-off commands run from here with <span class="text-fg">Run command</span>.
      </EmptyState>
      <div v-else class="overflow-x-auto">
        <table class="data-table stack" aria-label="Jobs">
          <thead>
            <tr>
              <th>Job</th>
              <th>Schedule</th>
              <th>Last run</th>
              <th>Next run</th>
              <th class="right">
                <span class="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="job in jobs.data.value" :key="job.name">
              <td data-primary>
                <span class="mono font-medium">{{ job.name }}</span>
                <span class="mono block truncate text-xs text-fg-muted" :title="formatArgv(job.command)">{{ formatArgv(job.command) }}</span>
              </td>
              <td data-label="Schedule" class="mono whitespace-nowrap" :title="`Five cron fields, read in UTC · timeout ${tidyDuration(job.timeout)}`">
                {{ job.schedule }} <span class="font-sans text-xs text-fg-subtle">UTC</span>
              </td>
              <td data-label="Last run">
                <template v-if="job.last_run">
                  <button type="button" class="inline-flex flex-wrap items-center gap-x-2 text-left hover:underline" :aria-label="`${describeRunOutcome(job.last_run)}: the last run of ${job.name}`" @click="openRunId = job.last_run.id">
                    <StatusBadge :tone="runStatusDisplay(job.last_run.status).tone" :label="describeRunOutcome(job.last_run)" :raw="job.last_run.status" />
                    <TimeAgo :time="job.last_run.started_at" class="text-fg-muted" />
                  </button>
                </template>
                <span v-else class="text-fg-subtle">never</span>
              </td>
              <td data-label="Next run" class="text-fg-muted" :title="job.next_run_at ?? undefined">
                {{ formatNextRun(job.next_run_at, now) }}
              </td>
              <td class="right">
                <UiButton
                  size="sm"
                  :disabled="!props.mayDeploy || props.busy || job.last_run?.status === 'running'"
                  :pending="startingJob === job.name"
                  :title="runTitleFor(job)"
                  :aria-label="`Run now: ${job.name}`"
                  @click="runNow(job)"
                >
                  <UiIcon name="play" :size="12" />
                  Run now
                </UiButton>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Run history -->
      <div>
        <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 bg-subtle px-4 py-2">
          <span class="label">Runs</span>
          <label class="flex items-center gap-2 text-xs text-fg-muted">
            Show
            <select v-model="filter" class="input mono !h-7 !w-auto min-w-32 !text-xs">
              <option value="">all</option>
              <option v-for="name in filterOptions" :key="name" :value="name">{{ name }}</option>
            </select>
          </label>
        </div>
        <TableSkeleton v-if="runs.loading.value && !runs.data.value" :rows="3" :columns="5" />
        <ErrorState
          v-else-if="runs.error.value && !runs.data.value"
          :error="runs.error.value"
          subject="the runs"
          :retrying="runs.refreshing.value"
          @retry="runs.refresh()"
        />
        <EmptyState v-else-if="(runs.data.value?.length ?? 0) === 0" title="No runs yet">
          Runs of the pre-deploy hook, of scheduled jobs and of one-off commands are listed here, newest first.
        </EmptyState>
        <div v-else class="overflow-x-auto">
          <table class="data-table stack" aria-label="Runs">
            <thead>
              <tr>
                <th class="w-16">
                  Run
                </th>
                <th>Command</th>
                <th>Started by</th>
                <th>Outcome</th>
                <th>Started</th>
                <th class="right">
                  Duration
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in runs.data.value" :key="r.id" class="clickable" @click="openRow(r, $event)">
                <td data-primary>
                  <button type="button" class="mono link" :aria-label="`Run ${r.id}, ${runTitle(r)}`" @click="openRunId = r.id">#{{ r.id }}</button>
                  <span class="mono ml-2 rows:hidden">{{ runTitle(r) }}</span>
                </td>
                <td class="mono cards:!hidden" :title="formatArgv(r.command)">
                  <span class="block max-w-md truncate">{{ runTitle(r) }}</span>
                </td>
                <td data-label="Started by" class="text-fg-muted">
                  {{ KIND_LABEL[r.kind] ?? r.kind }}
                </td>
                <td data-label="Outcome">
                  <StatusBadge :tone="runStatusDisplay(r.status).tone" :label="describeRunOutcome(r)" :raw="r.status" />
                </td>
                <td data-label="Started" class="text-fg-muted">
                  <TimeAgo :time="r.started_at" />
                </td>
                <td data-label="Duration" class="mono right text-fg-muted">
                  {{ runDuration(r) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <RunDialog :open="openRunId !== null" :application="props.application" :run-id="openRunId" @close="closeRun" />
    <RunCommandDialog :open="commandOpen" :application="props.application" :image="props.image" @close="commandOpen = false" @started="onCommandStarted" />
  </UiPanel>
</template>
