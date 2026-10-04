<script setup lang="ts">
import type { Application, Deployment } from '~/types/api'
import { diskDisplay } from '~/utils/alerts'
import { stateBackupDisplay } from '~/utils/backups'
import { formatBytes, pluralize } from '~/utils/format'
import { attentionReason } from '~/utils/marks'
import { statusCounts, verdict as judge } from '~/utils/overview'
import type { Tone } from '~/utils/status'
import { applicationStatusDisplay, needsAttention, sortBySeverity } from '~/utils/status'

useHead({ title: 'Overview' })

const agent = useAgent()
const apps = usePolling<Application[]>(signal => agent.get<Application[]>('/applications', { signal }))
const deployments = usePolling<Deployment[]>(signal => agent.get<Deployment[]>('/deployments', { query: { limit: 8 }, signal }))
const server = useServerInfo()
const access = useAccess()
const now = useNow()

const alerts = computed(() => server.data.value?.alerts ?? [])

/** "Is everything fine?", answered before anything else on the page. */
const verdict = computed(() => (apps.data.value ? judge(apps.data.value, alerts.value) : null))
const empty = computed(() => apps.data.value?.length === 0)

const counts = computed(() => statusCounts(apps.data.value ?? []).map(c => ({ ...c, ...applicationStatusDisplay(c.status) })))
const attention = computed(() => sortBySeverity((apps.data.value ?? []).filter(needsAttention)))

const DOT: Record<Tone, string> = { ok: 'bg-ok-dot', warn: 'bg-warn-dot', danger: 'bg-danger-dot', muted: 'bg-muted-dot' }
const TEXT: Record<Tone, string> = { ok: 'text-ok', warn: 'text-warn', danger: 'text-danger', muted: 'text-fg-muted' }
const VERDICT_ICON = { ok: 'check', warn: 'alert', danger: 'x-circle', muted: 'applications' } as const

// The server in four facts; its page has the rest.
const proxy = computed(() => {
  const p = server.data.value?.proxy
  if (!p) return null
  if (!p.enabled) return { tone: 'muted' as Tone, label: 'Off' }
  if (!p.reachable) return { tone: 'danger' as Tone, label: 'Unreachable' }
  return { tone: 'ok' as Tone, label: `Running, ${pluralize(p.routes, 'route')}` }
})
const disk = computed(() => diskDisplay(server.data.value?.disk, alerts.value))
const stateBackup = computed(() => (server.data.value?.backups ? stateBackupDisplay(server.data.value.backups, now.value) : null))

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

        <!-- The answer first -->
        <div class="min-h-12">
          <template v-if="verdict">
            <p class="flex items-start gap-2.5 text-lg font-semibold" :class="TEXT[verdict.tone]">
              <UiIcon :name="VERDICT_ICON[verdict.tone]" :size="18" class="mt-[3px]" />
              <span class="text-fg">{{ verdict.title }}</span>
            </p>
            <p class="mt-1 pl-7 text-fg-muted">
              <template v-if="!empty">
                {{ pluralize(apps.data.value?.length ?? 0, 'application') }}: {{ verdict.detail }}.
              </template>
              <template v-else>
                {{ verdict.detail }}
              </template>
            </p>
          </template>
          <div v-else class="space-y-2" role="progressbar" aria-busy="true" aria-label="Loading">
            <span class="skeleton h-5 w-72 max-w-full" />
            <span class="skeleton w-56 max-w-full" />
          </div>
        </div>

        <!-- What the agent says needs a look right now; the server page lists the same. -->
        <AlertList v-if="alerts.length > 0" :alerts="alerts" />

        <!-- An empty server: the way to the first deployment, which is the CLI's. -->
        <UiPanel v-if="empty" title="Deploy the first application">
          <ol class="divide-y divide-line">
            <li class="grid gap-x-6 gap-y-2 px-4 py-3.5 sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
              <div>
                <p class="font-medium">
                  1. Sign the CLI in to this server
                </p>
                <p class="mt-0.5 text-fg-muted">
                  On your machine. It asks for a token: the one you signed in here with works.
                </p>
              </div>
              <code class="command self-start">shipwick login --url https://&lt;the agent's hostname&gt;</code>
            </li>
            <li class="grid gap-x-6 gap-y-2 px-4 py-3.5 sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
              <div>
                <p class="font-medium">
                  2. Describe the application
                </p>
                <p class="mt-0.5 text-fg-muted">
                  In the project directory. It writes a Dockerfile and a deploy.yaml for a Node, .NET, Go, Python or static project.
                </p>
              </div>
              <code class="command self-start">shipwick init</code>
            </li>
            <li class="grid gap-x-6 gap-y-2 px-4 py-3.5 sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
              <div>
                <p class="font-medium">
                  3. Deploy it
                </p>
                <p class="mt-0.5 text-fg-muted">
                  The application appears here as soon as the deployment starts, and this page follows it.
                </p>
              </div>
              <code class="command self-start">shipwick deploy</code>
            </li>
            <!-- An image that is in a registry already needs no project directory: its deploy.yaml can be pasted here. -->
            <li v-if="access.knownTo('deploy')" class="grid gap-x-6 gap-y-2 px-4 py-3.5 sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
              <div>
                <p class="font-medium">
                  Or deploy an image from here
                </p>
                <p class="mt-0.5 text-fg-muted">
                  For an image that is in a registry already: paste its deploy.yaml and deploy it from this page.
                </p>
              </div>
              <div class="self-start">
                <UiButton variant="primary" size="sm" to="/deploy">
                  New application
                </UiButton>
              </div>
            </li>
          </ol>
        </UiPanel>

        <UiPanel v-if="!empty && (apps.loading.value || attention.length > 0)" title="Needs attention" :meta="apps.loading.value ? null : attention.length">
          <TableSkeleton v-if="apps.loading.value" :rows="2" :columns="4" />
          <table v-else class="data-table stack" aria-label="Applications that need attention">
            <thead>
              <tr>
                <th>Application</th>
                <th>Status</th>
                <th>What is wrong</th>
                <th>Version</th>
                <th class="right">
                  Since
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="app in attention" :key="app.name" class="clickable" @click="navigateTo(`/applications/${app.name}`)">
                <td data-primary>
                  <NuxtLink :to="`/applications/${app.name}`" class="mono font-medium hover:underline" @click.stop>{{ app.name }}</NuxtLink>
                </td>
                <td data-label="Status">
                  <span class="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
                    <StatusBadge v-bind="applicationStatusDisplay(app.status)" :raw="app.status" />
                    <ApplicationMarks :application="app" />
                  </span>
                </td>
                <td data-label="What is wrong" class="text-fg-muted">
                  {{ attentionReason(app) }}
                </td>
                <td data-label="Version" class="mono">
                  {{ app.version || '—' }}
                </td>
                <td data-label="Since" class="right text-fg-muted">
                  <TimeAgo :time="app.updated_at" />
                </td>
              </tr>
            </tbody>
          </table>
        </UiPanel>

        <!-- Counts by status: a single ruled strip, not a grid of cards -->
        <UiPanel v-if="!empty" title="Applications" :meta="apps.data.value?.length ?? null">
          <template #actions>
            <NuxtLink to="/applications" class="link text-xs text-fg-muted">
              All applications
            </NuxtLink>
          </template>
          <dl class="grid grid-cols-2 divide-line max-sm:divide-y sm:grid-cols-4 lg:grid-cols-7 lg:divide-x">
            <div v-for="item in counts" :key="item.status" class="flex items-baseline justify-between gap-2 px-4 py-3 lg:flex-col lg:items-start lg:justify-start lg:gap-1">
              <dt class="flex items-center gap-1.5 text-xs text-fg-muted">
                <span class="size-1.5 rounded-full" :class="item.count > 0 ? DOT[item.tone] : 'bg-muted-dot opacity-50'" aria-hidden="true" />
                {{ item.label }}
              </dt>
              <dd class="mono text-xl leading-none" :class="item.count === 0 ? 'text-fg-subtle' : 'text-fg'">
                <span v-if="apps.loading.value" class="skeleton mt-1 h-4 w-5" />
                <template v-else>
                  {{ item.count }}
                </template>
              </dd>
            </div>
          </dl>
        </UiPanel>

        <UiPanel title="Server">
          <template #actions>
            <NuxtLink to="/servers" class="link text-xs text-fg-muted">
              Server status
            </NuxtLink>
          </template>
          <dl v-if="server.data.value" class="facts grid-cols-2 lg:grid-cols-4">
            <div>
              <dt class="label">
                Name
              </dt>
              <dd class="mono mt-0.5 truncate" :title="server.data.value.hostname">
                {{ server.data.value.hostname }}
              </dd>
              <dd class="text-xs text-fg-subtle">
                {{ server.data.value.cpus }} CPUs · {{ formatBytes(server.data.value.memory_bytes) }} memory
              </dd>
            </div>
            <div>
              <dt class="label">
                Disk
              </dt>
              <dd v-if="disk" class="mt-0.5">
                <span class="mono" :class="disk.tone === 'danger' ? 'text-danger' : disk.tone === 'warn' ? 'text-warn' : ''">{{ disk.percent }}%</span> <span class="text-fg-muted">used</span>
              </dd>
              <dd v-else class="mt-0.5 text-fg-muted">
                Not measured
              </dd>
              <dd v-if="disk" class="text-xs text-fg-subtle">
                {{ disk.free }}
              </dd>
            </div>
            <div>
              <dt class="label">
                Reverse proxy
              </dt>
              <dd class="mt-0.5">
                <StatusBadge v-if="proxy" :tone="proxy.tone" :label="proxy.label" :raw="server.data.value.proxy.error || undefined" />
              </dd>
            </div>
            <div>
              <dt class="label">
                Backup of the agent's data
              </dt>
              <dd class="mt-0.5">
                <StatusBadge v-if="stateBackup" :tone="stateBackup.tone" :label="stateBackup.label" />
                <span v-else class="text-fg-muted">Not reported by this agent</span>
              </dd>
            </div>
          </dl>
          <p v-else-if="server.error.value" class="px-4 py-3 text-danger">
            The server did not answer: {{ server.error.value.displayMessage }}
          </p>
          <div v-else class="px-4 py-3" aria-busy="true">
            <span class="skeleton w-80 max-w-full" />
          </div>
        </UiPanel>

        <UiPanel v-if="!empty" title="Latest deployments">
          <template #actions>
            <NuxtLink to="/deployments" class="link text-xs text-fg-muted">
              All deployments
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
          <div v-else class="overflow-x-auto">
            <DeploymentsTable :deployments="deployments.data.value ?? []" />
          </div>
        </UiPanel>
      </div>
    </PageBody>
  </div>
</template>
