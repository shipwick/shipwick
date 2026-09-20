<script setup lang="ts">
import type { AgentEvent, ApplicationDetail, Container, Deployment } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { METRICS_HISTORY_SAMPLES } from '~/composables/useMetricsHistory'
import { formatBytes, formatCores, formatMemoryUsage, formatPercent, splitImage } from '~/utils/format'
import { rollbackCandidates } from '~/utils/deployments'
import { applicationStatusDisplay, containerStateDisplay, replicaHealthDisplay } from '~/utils/status'

const route = useRoute()
const router = useRouter()
const agent = useAgent()

const name = computed(() => String(route.params.name))
const path = computed(() => `/applications/${encodeURIComponent(name.value)}`)

useHead({ title: () => name.value })

// --- data ---------------------------------------------------------------------

// Follow a deployment in flight more closely: replicas come and go within seconds.
const deployingNow = ref(false)
const app = usePolling<ApplicationDetail>(signal => agent.get<ApplicationDetail>(path.value, { signal }), {
  interval: () => (deployingNow.value ? 2000 : 5000),
})
watch(() => app.data.value?.deploying, (deploying) => {
  deployingNow.value = Boolean(deploying)
})
const deployments = usePolling<Deployment[]>(signal => agent.get<Deployment[]>('/deployments', {
  query: { application: name.value, limit: 25 },
  signal,
}))
const events = usePolling<AgentEvent[]>(signal => agent.get<AgentEvent[]>(`${path.value}/events`, { query: { limit: 30 }, signal }))

const gone = computed(() => app.error.value?.notFound === true)
const hasActive = computed(() => Boolean(app.data.value?.active_deployment))
const metrics = useMetricsHistory(name, () => hasActive.value && !gone.value)

watch(name, () => {
  void app.reset()
  void deployments.reset()
  void events.reset()
})

function refreshAll() {
  void app.refresh()
  void deployments.refresh()
  void events.refresh()
}

// --- live deployment progress ---------------------------------------------------

const progress = useDeploymentProgress(() => refreshAll())
let dismissedId: number | null = null

// A deployment started elsewhere (CLI, CI, another tab) is picked up as well:
// the application itself says which deployment is in flight.
watch(() => app.data.value, (a) => {
  if (!a || progress.active.value) return
  const id = a.in_flight_deployment_id
  if (id !== null && id !== progress.progress.value?.deploymentId && id !== dismissedId) progress.followId(id)
})

function onStarted(deployment: Deployment) {
  progress.follow(deployment)
  refreshAll()
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

function dismissProgress() {
  dismissedId = progress.progress.value?.deploymentId ?? null
  progress.dismiss()
}

// --- actions ------------------------------------------------------------------

const dialog = ref<'deploy' | 'rollback' | 'stop' | 'delete' | null>(null)
const actionPending = ref(false)
const actionError = shallowRef<AgentError | null>(null)

const busy = computed(() => Boolean(app.data.value?.deploying) || progress.active.value)
const stopped = computed(() => app.data.value?.desired_state === 'stopped')
const rollbackTargets = computed(() => rollbackCandidates(deployments.data.value ?? [], name.value))

async function setRunning(run: boolean) {
  actionPending.value = true
  actionError.value = null
  try {
    app.data.value = await agent.post<ApplicationDetail>(`${path.value}/${run ? 'start' : 'stop'}`)
    dialog.value = null
    void events.refresh()
  }
  catch (cause) {
    actionError.value = toAgentError(cause)
  }
  finally {
    actionPending.value = false
  }
}

function closeDialog() {
  dialog.value = null
  actionError.value = null
}

// --- presentation -------------------------------------------------------------

const status = computed(() => (app.data.value ? applicationStatusDisplay(app.data.value.status) : null))
const spec = computed(() => app.data.value?.spec ?? null)
const envNames = computed(() => Object.keys(spec.value?.env ?? {}).sort())

const headline = computed(() => {
  const a = app.data.value
  if (!a) return ''
  const loop = a.containers.find(c => c.crash_loop)
  if (loop) return `Replica ${loop.replica} is crash-looping`
  if (a.status === 'FAILED') return 'No deployment has succeeded yet'
  if (a.status === 'DEPLOYING') return 'First deployment in progress'
  if (a.status === 'STOPPED') return 'Stopped on request'
  return `${a.replicas.healthy}/${a.replicas.desired} healthy`
})

const cpuValues = computed(() => metrics.samples.value.map(s => s.cpu_percent))
const memoryValues = computed(() => metrics.samples.value.map(s => s.memory_bytes))
const sampleTimes = computed(() => metrics.samples.value.map(s => s.collected_at))

// Limits come with the sample (percent of one core, summed over the replicas
// that were measured; 0 = unlimited), so they stay right while a rollout
// changes the number of containers.
const cpuCeiling = computed(() => metrics.latest.value?.cpu_limit_percent || null)
const memoryCeiling = computed(() => metrics.latest.value?.memory_limit_bytes || null)

function replicaMetrics(container: Container) {
  return metrics.latest.value?.replicas.find(r => r.container === container.name) ?? null
}

function replicaCpuTitle(container: Container): string | undefined {
  const limit = replicaMetrics(container)?.cpu_limit_percent
  return limit ? `Limit ${formatPercent(limit)} (percent of one core)` : undefined
}

/** By replica, the older deployment's container first: a stable order while a rollout swaps them. */
const containers = computed(() => [...(app.data.value?.containers ?? [])].sort((a, b) =>
  a.replica - b.replica || a.deployment_id - b.deployment_id))

/** During a rollout the replicas of two deployments run side by side; say which is which. */
const mixedVersions = computed(() => new Set((app.data.value?.containers ?? []).map(c => c.deployment_id)).size > 1)
const sequenceById = computed(() => new Map((deployments.data.value ?? []).map(d => [d.id, d.sequence])))

// A domain is only served if the agent has a reverse proxy, and can reach it.
const server = useServerInfo()
const proxyProblem = computed(() => {
  const proxy = server.data.value?.proxy
  if (!proxy) return '' // server info not loaded yet
  if (!proxy.enabled) return 'Not served: no reverse proxy is configured on the agent'
  if (!proxy.reachable) return 'The reverse proxy is unreachable; routing may be stale'
  return ''
})

const healthCheck = computed(() => {
  const h = spec.value?.health
  if (!h) return null
  return `GET ${h.path} every ${h.interval}, timeout ${h.timeout}, ${h.retries} retries`
})
</script>

<template>
  <div>
    <PageHeader :crumbs="[{ label: 'Applications', to: '/applications' }, { label: name, mono: true }]">
      <template v-if="app.data.value" #actions>
        <UiButton
          variant="primary"
          size="sm"
          :disabled="busy || !hasActive"
          :title="!hasActive ? 'Nothing deployed yet. The first deployment needs shipwick deploy.' : busy ? 'A deployment is in progress' : undefined"
          @click="dialog = 'deploy'"
        >
          Deploy
        </UiButton>
        <UiButton
          size="sm"
          :disabled="busy || rollbackTargets.length === 0"
          :title="rollbackTargets.length === 0 ? 'No earlier successful deployment' : undefined"
          @click="dialog = 'rollback'"
        >
          Rollback
        </UiButton>
        <UiButton v-if="stopped" size="sm" :disabled="busy || !hasActive" :pending="actionPending && dialog === null" @click="setRunning(true)">
          <UiIcon name="play" :size="12" />
          Start
        </UiButton>
        <UiButton v-else size="sm" :disabled="busy || !hasActive" @click="dialog = 'stop'">
          <UiIcon name="pause" :size="12" />
          Stop
        </UiButton>
        <UiButton variant="danger" size="sm" :disabled="busy" @click="dialog = 'delete'">
          <UiIcon name="trash" :size="12" />
          Delete
        </UiButton>
      </template>
    </PageHeader>

    <PageBody>
      <!-- Loading -->
      <div v-if="app.loading.value && !app.data.value" class="space-y-3" aria-busy="true" aria-label="Loading">
        <span class="skeleton h-6 w-40" />
        <span class="skeleton w-24" />
        <span class="skeleton w-64" />
      </div>

      <!-- Not found / unreachable with nothing to show -->
      <div v-else-if="!app.data.value && app.error.value" class="rounded-sm border border-line">
        <EmptyState v-if="gone" :title="`No application named ${name}`">
          It may have been deleted. <NuxtLink to="/applications" class="link">
            Back to applications
          </NuxtLink>
        </EmptyState>
        <ErrorState v-else :error="app.error.value" subject="the application" :retrying="app.refreshing.value" @retry="refreshAll" />
      </div>

      <div v-else-if="app.data.value && status" class="space-y-8">
        <StaleNotice v-if="!gone" :error="app.error.value" :updated-at="app.updatedAt.value" class="!mb-0" />
        <p v-if="gone" class="rounded-sm border border-danger-line bg-danger-bg px-3 py-2 text-xs text-danger" role="alert">
          This application no longer exists on the agent. <NuxtLink to="/applications" class="underline">
            Back to applications
          </NuxtLink>
        </p>
        <InlineError v-if="dialog === null" :error="actionError" />

        <!-- Summary: identity on the left, live numbers on the right -->
        <div class="grid gap-6 lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
          <div class="min-w-0">
            <p class="mono truncate text-xl font-semibold">
              {{ app.data.value.name }}
            </p>
            <div class="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1">
              <StatusBadge :tone="status.tone" :label="status.label" :raw="app.data.value.status" size="md" />
              <span v-if="app.data.value.deploying && app.data.value.status !== 'DEPLOYING'" class="label !text-warn">Deploying</span>
              <span class="text-fg-muted">{{ headline }}</span>
            </div>
            <dl class="mt-4 grid grid-cols-[5.5rem_minmax(0,1fr)] gap-x-3 gap-y-1.5">
              <dt class="label pt-0.5">
                Version
              </dt>
              <dd class="mono">
                {{ app.data.value.version || '—' }}
                <span v-if="app.data.value.active_deployment" class="font-sans text-fg-muted">
                  · deployed <TimeAgo :time="app.data.value.active_deployment.completed_at ?? app.data.value.active_deployment.started_at" />
                  · <NuxtLink :to="`/deployments/${app.data.value.active_deployment.id}`" class="mono link">#{{ app.data.value.active_deployment.sequence }}</NuxtLink>
                </span>
              </dd>
              <dt class="label pt-0.5">
                Image
              </dt>
              <dd class="mono break-all">
                {{ app.data.value.image || '—' }}
              </dd>
              <dt class="label pt-0.5">
                Domain
              </dt>
              <dd>
                <a
                  v-if="app.data.value.domain"
                  :href="`https://${app.data.value.domain}`"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="mono link inline-flex items-center gap-1"
                >{{ app.data.value.domain }}<UiIcon name="external" :size="12" /></a>
                <span v-else class="text-fg-muted">Not routed</span>
                <span v-if="app.data.value.domain && proxyProblem" class="ml-2 text-xs text-warn">{{ proxyProblem }}</span>
              </dd>
            </dl>
          </div>

          <div class="min-w-0 self-start divide-y divide-line rounded-sm border border-line">
            <div class="grid grid-cols-[4.5rem_minmax(0,1fr)] items-center gap-x-4 px-4 py-2.5 sm:grid-cols-[4.5rem_11rem_minmax(0,1fr)]">
              <span class="label">CPU</span>
              <span class="mono">
                {{ formatPercent(metrics.latest.value?.cpu_percent) }}
                <span v-if="metrics.latest.value && cpuCeiling" class="text-fg-subtle">/ {{ formatPercent(cpuCeiling) }}</span>
              </span>
              <Sparkline
                v-if="metrics.availability.value === 'available'"
                class="max-sm:col-span-2 max-sm:mt-1.5"
                label="CPU"
                :values="cpuValues"
                :times="sampleTimes"
                :slots="METRICS_HISTORY_SAMPLES"
                :ceiling="cpuCeiling"
                :format="formatPercent"
              />
              <span v-else class="text-xs text-fg-subtle max-sm:col-span-2">{{ metrics.availability.value === 'loading' ? '' : metrics.reason.value }}</span>
            </div>
            <div class="grid grid-cols-[4.5rem_minmax(0,1fr)] items-center gap-x-4 px-4 py-2.5 sm:grid-cols-[4.5rem_11rem_minmax(0,1fr)]">
              <span class="label">Memory</span>
              <span class="mono">{{ formatMemoryUsage(metrics.latest.value?.memory_bytes, metrics.latest.value?.memory_limit_bytes) }}</span>
              <Sparkline
                v-if="metrics.availability.value === 'available'"
                class="max-sm:col-span-2 max-sm:mt-1.5"
                label="Memory"
                :values="memoryValues"
                :times="sampleTimes"
                :slots="METRICS_HISTORY_SAMPLES"
                :ceiling="memoryCeiling"
                :format="formatBytes"
              />
            </div>
            <div class="grid grid-cols-[4.5rem_minmax(0,1fr)] items-center gap-x-4 px-4 py-2.5">
              <span class="label">Replicas</span>
              <span class="mono">
                {{ app.data.value.replicas.healthy }} / {{ app.data.value.replicas.desired }}
                <span class="font-sans text-fg-muted">healthy</span>
                <span class="font-sans text-fg-subtle"> · {{ app.data.value.replicas.running }} running</span>
              </span>
            </div>
          </div>
        </div>

        <DeploymentProgressPanel
          v-if="progress.progress.value"
          :progress="progress.progress.value"
          :still-running="app.data.value.version"
          :app-status="app.data.value.status"
          @dismiss="dismissProgress"
        />

        <UiPanel title="Replicas" :meta="app.data.value.containers.length">
          <EmptyState v-if="app.data.value.containers.length === 0" title="No containers">
            Nothing is running for this application.
          </EmptyState>
          <div v-else class="overflow-x-auto">
            <table class="data-table stack">
              <thead>
                <tr>
                  <th class="w-20">
                    Replica
                  </th>
                  <th>Container</th>
                  <th>State</th>
                  <th>Health</th>
                  <th>Restarts</th>
                  <th class="right">
                    CPU
                  </th>
                  <th class="right">
                    Memory
                  </th>
                  <th class="right">
                    Started
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="c in containers" :key="c.id">
                  <td data-primary class="mono">
                    {{ c.replica }}<span class="ml-2 text-fg-muted sm:hidden">{{ c.name }}</span>
                  </td>
                  <td class="mono max-sm:!hidden" :title="`${c.id.slice(0, 12)} · ${c.ip || 'no address'}`">
                    {{ c.name }}
                    <span v-if="mixedVersions" class="ml-2 text-fg-subtle" :title="c.image">
                      {{ splitImage(c.image).tag || 'latest' }}<template v-if="sequenceById.get(c.deployment_id)"> · #{{ sequenceById.get(c.deployment_id) }}</template>
                    </span>
                  </td>
                  <td data-label="State">
                    <StatusBadge v-bind="containerStateDisplay(c)" :raw="c.state" />
                  </td>
                  <td data-label="Health">
                    <StatusBadge v-bind="replicaHealthDisplay(c.health)" :raw="c.health || 'no health check configured'" />
                  </td>
                  <td data-label="Restarts" class="mono" :class="c.crash_loop ? 'text-danger' : c.restarts > 0 ? 'text-warn' : 'text-fg-muted'">
                    <span>{{ c.restarts }}<span v-if="c.crash_loop" class="font-sans"> (crash loop)</span></span>
                  </td>
                  <td data-label="CPU" class="mono right text-fg-muted" :title="replicaCpuTitle(c)">
                    {{ formatPercent(replicaMetrics(c)?.cpu_percent) }}
                  </td>
                  <td data-label="Memory" class="mono right text-fg-muted">
                    {{ formatMemoryUsage(replicaMetrics(c)?.memory_bytes, replicaMetrics(c)?.memory_limit_bytes) }}
                  </td>
                  <td data-label="Started" class="right text-fg-muted">
                    <TimeAgo :time="c.started_at" />
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </UiPanel>

        <UiPanel v-if="spec" title="Configuration">
          <dl class="grid gap-px bg-line sm:grid-cols-2 lg:grid-cols-3">
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Health check
              </dt>
              <dd class="mono mt-0.5">
                {{ healthCheck ?? 'None: replicas only need to stay up' }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Limits per replica
              </dt>
              <dd class="mono mt-0.5">
                CPU {{ formatCores(spec.resources.cpu) }} · memory {{ spec.resources.memory_bytes ? formatBytes(spec.resources.memory_bytes) : 'unlimited' }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Port · replicas
              </dt>
              <dd class="mono mt-0.5">
                {{ spec.port ?? '—' }} · {{ spec.replicas }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Restart policy
              </dt>
              <dd class="mono mt-0.5">
                {{ spec.restart.policy }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Strategy
              </dt>
              <dd class="mono mt-0.5">
                {{ spec.deploy.strategy }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Environment
              </dt>
              <dd class="mono mt-0.5 break-words" :title="envNames.length ? 'Values are never returned by the agent' : undefined">
                {{ envNames.length ? envNames.join(', ') : 'No variables' }}
              </dd>
            </div>
          </dl>
        </UiPanel>

        <UiPanel title="Deployments">
          <template #actions>
            <NuxtLink :to="{ path: '/deployments', query: { application: name } }" class="link text-xs text-fg-muted">
              View all
            </NuxtLink>
          </template>
          <TableSkeleton v-if="deployments.loading.value" :rows="4" :columns="5" />
          <ErrorState
            v-else-if="deployments.error.value && !deployments.data.value"
            :error="deployments.error.value"
            subject="deployments"
            :retrying="deployments.refreshing.value"
            @retry="deployments.refresh()"
          />
          <EmptyState v-else-if="(deployments.data.value?.length ?? 0) === 0" title="No deployments" />
          <div v-else class="overflow-x-auto">
            <DeploymentsTable :deployments="deployments.data.value ?? []" :show-application="false" />
          </div>
        </UiPanel>

        <UiPanel title="Events">
          <div v-if="events.loading.value" class="space-y-2 px-4 py-3" aria-busy="true">
            <span class="skeleton w-2/3" /><span class="skeleton w-1/2" />
          </div>
          <ErrorState
            v-else-if="events.error.value && !events.data.value"
            :error="events.error.value"
            subject="events"
            :retrying="events.refreshing.value"
            @retry="events.refresh()"
          />
          <EmptyState v-else-if="(events.data.value?.length ?? 0) === 0" title="No events">
            Crashes, restarts, health changes, stops and starts are recorded here.
          </EmptyState>
          <div v-else class="max-h-80 overflow-y-auto">
            <EventList :events="events.data.value ?? []" />
          </div>
        </UiPanel>

        <UiPanel title="Logs" :bordered="false">
          <template #actions>
            <NuxtLink :to="{ path: '/logs', query: { application: name } }" class="link text-xs text-fg-muted">
              Open in Logs
            </NuxtLink>
          </template>
          <LogViewer v-if="app.data.value.containers.length > 0" :application="name" height-class="h-80" />
          <div v-else class="rounded-sm border border-line">
            <EmptyState title="No logs">
              There are no containers to read logs from.
            </EmptyState>
          </div>
        </UiPanel>
      </div>
    </PageBody>

    <template v-if="app.data.value">
      <DeployDialog :open="dialog === 'deploy'" :application="app.data.value" @close="closeDialog" @started="onStarted" />
      <RollbackDialog
        :open="dialog === 'rollback'"
        :application="app.data.value"
        :deployments="deployments.data.value ?? []"
        @close="closeDialog"
        @started="onStarted"
        @stale="deployments.refresh()"
      />
      <ConfirmDialog
        :open="dialog === 'stop'"
        :title="`Stop ${name}`"
        confirm-label="Stop application"
        danger
        :pending="actionPending"
        :error="actionError"
        @close="closeDialog"
        @confirm="setRunning(false)"
      >
        All replicas are stopped<template v-if="app.data.value.domain">
          and <span class="mono text-fg">{{ app.data.value.domain }}</span> stops answering
        </template>. The application stays stopped, across agent restarts too, until you start it again. Nothing is deleted.
      </ConfirmDialog>
      <DeleteDialog :open="dialog === 'delete'" :name="name" @close="closeDialog" @deleted="router.replace('/applications')" />
    </template>
  </div>
</template>
