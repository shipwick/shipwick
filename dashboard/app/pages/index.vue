<script setup lang="ts">
import type { Application, ApplicationStatus, Deployment } from '~/types/api'
import { formatBytes, pluralize } from '~/utils/format'
import type { Tone } from '~/utils/status'
import { applicationStatusDisplay, needsAttention, replicaSummary, sortBySeverity } from '~/utils/status'

useHead({ title: 'Overview' })

const agent = useAgent()
const apps = usePolling<Application[]>(signal => agent.get<Application[]>('/applications', { signal }))
const deployments = usePolling<Deployment[]>(signal => agent.get<Deployment[]>('/deployments', { query: { limit: 10 }, signal }))
const server = useServerInfo()

const STATUS_ORDER: ApplicationStatus[] = ['HEALTHY', 'DEGRADED', 'DOWN', 'CRASH_LOOP', 'DEPLOYING', 'FAILED', 'STOPPED']

const counts = computed(() => {
  const list = apps.data.value ?? []
  return STATUS_ORDER.map((status) => {
    const display = applicationStatusDisplay(status)
    return { status, ...display, count: list.filter(a => a.status === status).length }
  })
})

const attention = computed(() => sortBySeverity((apps.data.value ?? []).filter(needsAttention)))

const DOT: Record<Tone, string> = { ok: 'bg-ok-dot', warn: 'bg-warn-dot', danger: 'bg-danger-dot', muted: 'bg-muted-dot' }

/** One factual sentence about what is wrong, in the CLI's voice. */
function reason(app: Application): string {
  switch (app.status) {
    case 'CRASH_LOOP': return `A replica is crash-looping; ${replicaSummary(app.replicas)}`
    case 'DOWN': return `No healthy replica; ${app.replicas.running}/${app.replicas.desired} running`
    case 'DEGRADED': return replicaSummary(app.replicas)
    case 'DEPLOYING': return 'First deployment in progress'
    case 'FAILED': return 'No deployment has succeeded yet'
    default: return replicaSummary(app.replicas)
  }
}

const proxy = computed(() => {
  const p = server.data.value?.proxy
  if (!p) return null
  if (!p.enabled) return { tone: 'muted' as Tone, label: 'Proxy disabled' }
  if (!p.reachable) return { tone: 'danger' as Tone, label: 'Proxy unreachable' }
  return { tone: 'ok' as Tone, label: `Proxy up, ${pluralize(p.routes, 'route')}` }
})

function retryAll() {
  void apps.refresh()
  void deployments.refresh()
  void server.refresh()
}
</script>

<template>
  <div>
    <PageHeader :crumbs="[{ label: 'Overview' }]" />
    <PageBody>
      <ErrorState
        v-if="apps.error.value && !apps.data.value"
        :error="apps.error.value"
        subject="applications"
        :retrying="apps.refreshing.value"
        class="rounded-sm border border-line"
        @retry="retryAll"
      />

      <div v-else class="space-y-8">
        <StaleNotice :error="apps.error.value" :updated-at="apps.updatedAt.value" class="!mb-0" />

        <!-- Server summary line -->
        <div class="flex min-h-5 flex-wrap items-center gap-x-5 gap-y-1 text-fg-muted">
          <template v-if="server.data.value">
            <NuxtLink to="/servers" class="mono font-medium text-fg hover:underline">{{ server.data.value.hostname }}</NuxtLink>
            <span>Docker <span class="mono text-fg">{{ server.data.value.docker_version }}</span></span>
            <span><span class="mono text-fg">{{ server.data.value.cpus }}</span> CPUs</span>
            <span><span class="mono text-fg">{{ formatBytes(server.data.value.memory_bytes) }}</span> memory</span>
            <span><span class="mono text-fg">{{ server.data.value.containers }}</span> {{ server.data.value.containers === 1 ? 'container' : 'containers' }} running</span>
            <StatusBadge v-if="proxy" :tone="proxy.tone" :label="proxy.label" :raw="server.data.value.proxy.error || undefined" />
          </template>
          <span v-else-if="server.error.value" class="text-danger">Server facts unavailable: {{ server.error.value.message }}</span>
          <span v-else class="skeleton w-80 max-w-full" />
        </div>

        <!-- Counts by status: a single ruled strip, not a grid of cards -->
        <UiPanel title="Applications" :meta="apps.data.value?.length ?? null">
          <template #actions>
            <NuxtLink to="/applications" class="link text-xs text-fg-muted">
              View all
            </NuxtLink>
          </template>
          <dl class="grid grid-cols-2 divide-line max-sm:divide-y sm:grid-cols-4 lg:grid-cols-7 lg:divide-x">
            <div v-for="item in counts" :key="item.status" class="flex items-baseline justify-between gap-2 px-4 py-3 lg:flex-col lg:items-start lg:justify-start lg:gap-1">
              <dt class="flex items-center gap-1.5 text-xs text-fg-muted">
                <span class="size-1.5 rounded-full" :class="item.count > 0 ? DOT[item.tone] : 'bg-muted-dot opacity-50'" aria-hidden="true" />
                {{ item.label }}
              </dt>
              <dd class="mono text-xl leading-none" :class="item.count === 0 ? 'text-fg-faint' : 'text-fg'">
                <span v-if="apps.loading.value" class="skeleton mt-1 h-4 w-5" />
                <template v-else>
                  {{ item.count }}
                </template>
              </dd>
            </div>
          </dl>
        </UiPanel>

        <UiPanel title="Needs attention" :meta="apps.loading.value ? null : attention.length">
          <TableSkeleton v-if="apps.loading.value" :rows="2" :columns="4" />
          <EmptyState v-else-if="(apps.data.value?.length ?? 0) === 0" title="No applications yet">
            Deploy the first one from your project directory with <span class="mono text-fg">shipwick deploy</span>.
          </EmptyState>
          <p v-else-if="attention.length === 0" class="flex items-center gap-2 px-4 py-3 text-fg-muted">
            <UiIcon name="check" :size="14" class="text-ok" />
            Every running application is healthy.
          </p>
          <table v-else class="data-table stack">
            <thead>
              <tr>
                <th>Application</th>
                <th>Status</th>
                <th>What is wrong</th>
                <th>Version</th>
                <th class="right">
                  Updated
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="app in attention" :key="app.name" class="clickable" @click="navigateTo(`/applications/${app.name}`)">
                <td data-primary>
                  <NuxtLink :to="`/applications/${app.name}`" class="mono font-medium hover:underline" @click.stop>{{ app.name }}</NuxtLink>
                </td>
                <td data-label="Status">
                  <StatusBadge v-bind="applicationStatusDisplay(app.status)" :raw="app.status" />
                </td>
                <td data-label="Detail" class="text-fg-muted">
                  {{ reason(app) }}
                </td>
                <td data-label="Version" class="mono">
                  {{ app.version || '—' }}
                </td>
                <td data-label="Updated" class="right text-fg-muted">
                  <TimeAgo :time="app.updated_at" />
                </td>
              </tr>
            </tbody>
          </table>
        </UiPanel>

        <UiPanel title="Recent deployments">
          <template #actions>
            <NuxtLink to="/deployments" class="link text-xs text-fg-muted">
              View all
            </NuxtLink>
          </template>
          <TableSkeleton v-if="deployments.loading.value" :rows="5" :columns="6" />
          <ErrorState
            v-else-if="deployments.error.value && !deployments.data.value"
            :error="deployments.error.value"
            subject="deployments"
            :retrying="deployments.refreshing.value"
            @retry="deployments.refresh()"
          />
          <EmptyState v-else-if="(deployments.data.value?.length ?? 0) === 0" title="No deployments yet" />
          <DeploymentsTable v-else :deployments="deployments.data.value ?? []" />
        </UiPanel>
      </div>
    </PageBody>
  </div>
</template>
