<script setup lang="ts">
import type { Deployment, DeploymentDetail } from '~/types/api'
import { deriveProgress } from '~/utils/deploymentProgress'
import { durationBetween, formatAbsoluteUtc, formatBytes, formatCores, formatDuration } from '~/utils/format'
import { describeBackupPlan } from '~/utils/backups'
import { tidyDuration } from '~/utils/jobs'
import { archiveSupported, logsPath } from '~/utils/logArchive'
import { deniedRegistry } from '~/utils/registries'
import { describeBuild, describeHealth, describeLogging, describeStatic, formatArgv, formatHostname, formatPathRedirect, formatPublish, hostnamesOf } from '~/utils/spec'
import { deploymentStatusDisplay } from '~/utils/status'

const route = useRoute()
const agent = useAgent()

const id = computed(() => String(route.params.id))
const valid = computed(() => /^[1-9]\d*$/.test(id.value))

// Events never change once a deployment is done, so polling stops at completed_at.
const deployment = usePolling<DeploymentDetail>(
  signal => agent.get<DeploymentDetail>(`/deployments/${id.value}`, { signal }),
  { interval: 1000, enabled: valid, until: d => d.completed_at !== null },
)

watch(id, () => void deployment.reset())

const d = computed(() => deployment.data.value)
useHead({ title: () => (d.value ? `${d.value.application} #${d.value.sequence}` : `Deployment ${id.value}`) })

const status = computed(() => (d.value ? deploymentStatusDisplay(d.value.status) : null))
const progress = computed(() => (d.value ? deriveProgress(d.value) : null))
const events = computed(() => [...(d.value?.events ?? [])].sort((a, b) => a.id - b.id))
const envNames = computed(() => Object.keys(d.value?.spec.env ?? {}).sort())
const hostnames = computed(() => (d.value ? hostnamesOf(d.value.spec).map(formatHostname) : []))
const healthCheck = computed(() => describeHealth(d.value?.spec.health))
const published = computed(() => (d.value?.spec.publish ?? []).map(formatPublish))
const process = computed(() => {
  const s = d.value?.spec
  if (!s) return []
  const lines: { label: string, value: string }[] = []
  if (s.entrypoint?.length) lines.push({ label: 'Entrypoint', value: formatArgv(s.entrypoint) })
  if (s.command?.length) lines.push({ label: 'Command', value: formatArgv(s.command) })
  if (s.user) lines.push({ label: 'User', value: s.user })
  return lines
})
/** The proxy block as it was deployed, one line per thing it says. Accounts by name and path: there is no password to show. */
const proxyLines = computed(() => {
  const s = d.value?.spec
  if (!s) return []
  const lines: { label: string, value: string }[] = []
  if (s.path) lines.push({ label: 'Path', value: s.proxy?.strip_prefix ? `${s.path} (removed before the application sees the request)` : s.path })
  for (const [header, value] of Object.entries(s.proxy?.headers ?? {}).sort(([a], [b]) => a.localeCompare(b))) lines.push({ label: 'Header', value: `${header}: ${value}` })
  for (const account of s.proxy?.basic_auth ?? []) lines.push({ label: 'Account', value: `${account.username} for ${account.path || s.path || 'every path'}` })
  for (const redirect of s.proxy?.redirects ?? []) lines.push({ label: 'Redirect', value: formatPathRedirect(redirect) })
  return lines
})
/**
 * A deployment that failed took its replicas with it; from 0.7 on the agent
 * keeps what they wrote. The way to it, for a deployment that had replicas.
 */
const server = useServerInfo()
const replicaOutput = computed(() => {
  const dep = d.value
  if (!dep || !dep.completed_at || dep.static || !archiveSupported(server.data.value)) return null
  return dep.status === 'FAILED' || dep.status === 'ROLLED_BACK' ? logsPath(dep.application, { view: 'archive', deployment: dep.id }) : null
})
/** The registry a refused pull names: the way to the form that stores a credential for it. */
const registry = computed(() => deniedRegistry(d.value?.error ?? ''))
const logging = computed(() => describeLogging(d.value?.spec))
const loggingOptions = computed(() => Object.entries(d.value?.spec.logging?.options ?? {}).sort(([a], [b]) => a.localeCompare(b)))
/** A static deployment serves a folder: "42 files, 3.1 MB, served by the proxy" where a container deployment has an image. */
const isStatic = computed(() => Boolean(d.value?.static))
const servedFiles = computed(() => describeStatic(d.value?.static))
const buildOrigin = computed(() => describeBuild(d.value?.spec.build))

// A redeploy or rollback re-used another deployment's stored configuration.
// Deployment records are immutable enough for this: fetch the source once to
// show its number and version. If it cannot be loaded, the link still works.
const source = shallowRef<Deployment | null>(null)
let sourceRequest: AbortController | null = null
watch(() => d.value?.source_deployment_id ?? null, async (sourceId) => {
  sourceRequest?.abort()
  source.value = null
  if (sourceId === null) return
  const own = new AbortController()
  sourceRequest = own
  try {
    const found = await agent.get<DeploymentDetail>(`/deployments/${sourceId}`, { signal: own.signal })
    if (!own.signal.aborted) source.value = found
  }
  catch {
    // Not fatal: the origin is then shown without the source's number.
  }
}, { immediate: true })
onScopeDispose(() => sourceRequest?.abort())

const outcome = computed(() => {
  const dep = d.value
  if (!dep) return ''
  if (!dep.completed_at) return `${progress.value?.activity ?? 'Working'}…`
  switch (dep.status) {
    case 'ACTIVE': return 'Currently serving'
    case 'SUPERSEDED': return 'Served successfully; replaced by a later deployment'
    case 'FAILED': return dep.static ? 'Failed; the files served before are still served' : 'Failed before any replica of the previous version was replaced; nothing was affected'
    case 'ROLLED_BACK': return 'Failed part-way; the replicas already replaced were restored from the previous deployment'
    default: return ''
  }
})

const now = useNow()
const duration = computed(() => {
  if (!d.value) return '—'
  if (d.value.completed_at) return formatDuration(durationBetween(d.value.started_at, d.value.completed_at))
  return formatDuration(Math.max(0, Math.floor((now.value - Date.parse(d.value.started_at)) / 1000) * 1000))
})

const crumbs = computed(() => [
  { label: 'Deployments', to: '/deployments' },
  ...(d.value ? [{ label: d.value.application, to: `/applications/${d.value.application}`, mono: true }] : []),
  { label: d.value ? `#${d.value.sequence}` : id.value, mono: true },
])
</script>

<template>
  <div>
    <PageHeader :crumbs="crumbs">
      <template v-if="d" #actions>
        <UiButton size="sm" :to="`/applications/${d.application}`">
          Open application
        </UiButton>
      </template>
    </PageHeader>

    <PageBody>
      <div v-if="!valid" class="rounded-sm border border-line">
        <EmptyState title="Not a deployment id">
          <NuxtLink to="/deployments" class="link">
            Back to deployments
          </NuxtLink>
        </EmptyState>
      </div>

      <div v-else-if="deployment.loading.value && !d" class="space-y-3" role="progressbar" aria-busy="true" aria-label="Loading">
        <span class="skeleton h-6 w-48" />
        <span class="skeleton w-32" />
        <span class="skeleton w-72" />
      </div>

      <div v-else-if="!d && deployment.error.value" class="rounded-sm border border-line">
        <EmptyState v-if="deployment.error.value.notFound" :title="`No deployment with id ${id}`">
          Its application may have been deleted, which removes its history. <NuxtLink to="/deployments" class="link">
            Back to deployments
          </NuxtLink>
        </EmptyState>
        <ErrorState v-else :error="deployment.error.value" subject="the deployment" :retrying="deployment.refreshing.value" @retry="deployment.refresh()" />
      </div>

      <div v-else-if="d && status && progress" class="space-y-8">
        <StaleNotice :error="deployment.error.value" :updated-at="deployment.updatedAt.value" class="!mb-0" />

        <!-- Identity -->
        <div>
          <p class="text-xl font-semibold">
            <span class="mono">{{ d.application }}</span>
            <span class="mono text-fg-muted"> #{{ d.sequence }}</span>
          </p>
          <div class="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1">
            <StatusBadge :tone="status.tone" :label="status.label" :raw="d.status" size="md" />
            <span v-if="outcome" class="text-fg-muted">{{ outcome }}</span>
          </div>
          <p v-if="d.kind === 'rollback' || d.kind === 'redeploy'" class="mt-1.5">
            <span class="label mr-2">Origin</span>
            <DeploymentOrigin :deployment="d" :known="source ? [source] : []" />
            <span class="text-fg-subtle"> · its stored configuration was deployed again<template v-if="d.kind === 'redeploy' && source && source.image !== d.image">, with another image</template></span>
          </p>
          <p v-else-if="d.kind === 'import' || d.kind === 'standby'" class="mt-1.5">
            <span class="label mr-2">Origin</span>
            <DeploymentOrigin :deployment="d" :known="[]" />
            <span class="text-fg-subtle"> · its configuration came with an export of another server<template v-if="d.kind === 'standby'">; the application is deployed stopped and waits for a promotion</template></span>
          </p>
          <p v-if="d.error" class="mt-3 flex items-start gap-2 rounded-sm border border-danger-line bg-danger-bg px-3 py-2 font-medium text-danger">
            <UiIcon name="x-circle" :size="14" class="mt-[3px]" />
            <span class="min-w-0 break-words">{{ d.error }}</span>
          </p>
          <p v-if="replicaOutput" class="mt-1.5 text-fg-muted">
            What its containers wrote before they were removed is kept:
            <NuxtLink :to="replicaOutput" class="link">Output of its replicas</NuxtLink>.
          </p>
          <p v-if="registry" class="mt-1.5 text-fg-muted">
            If the image is private, the server needs a credential for <span class="mono text-fg">{{ registry }}</span>:
            <NuxtLink :to="{ path: '/settings/registries', query: { registry } }" class="link">log it in on the Registries page</NuxtLink>, then deploy again.
          </p>
        </div>

        <!-- Metadata -->
        <UiPanel title="Details">
          <dl class="facts sm:grid-cols-2 lg:grid-cols-4">
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Version
              </dt>
              <dd class="mono mt-0.5">
                {{ d.version || '—' }}
                <span v-if="isStatic" class="block font-sans text-fg-muted" :title="d.static?.digest">{{ servedFiles }}</span>
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Started
              </dt>
              <dd class="mt-0.5">
                <TimeAgo :time="d.started_at" /><span class="mono ml-2 text-xs text-fg-subtle">{{ formatAbsoluteUtc(d.started_at) }}</span>
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Completed
              </dt>
              <dd class="mt-0.5">
                <template v-if="d.completed_at">
                  <TimeAgo :time="d.completed_at" /><span class="mono ml-2 text-xs text-fg-subtle">{{ formatAbsoluteUtc(d.completed_at) }}</span>
                </template>
                <span v-else class="text-warn">In progress</span>
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Duration
              </dt>
              <dd class="mono mt-0.5">
                {{ duration }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5 sm:col-span-2">
              <dt class="label">
                Image
              </dt>
              <dd v-if="isStatic" class="mt-0.5 text-fg-muted">
                None: a folder served by the proxy, uploaded by <span class="mono text-fg">shipwick deploy</span>
              </dd>
              <dd v-else class="mono mt-0.5 break-all">
                {{ d.image }}
                <span v-if="buildOrigin" class="block font-sans text-fg-muted">{{ buildOrigin }}</span>
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                By
              </dt>
              <dd class="mono mt-0.5" :title="d.by ? `The token that started it` : 'Recorded before tokens had names'">
                {{ d.by ?? '—' }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Deployment id
              </dt>
              <dd class="mono mt-0.5">
                {{ d.id }}
              </dd>
            </div>
          </dl>
        </UiPanel>

        <!-- Spec -->
        <UiPanel title="Configuration deployed">
          <dl class="facts sm:grid-cols-2 lg:grid-cols-4">
            <!-- A static configuration is a folder and a hostname; the container settings do not exist for it. -->
            <div v-if="d.spec.static" class="bg-bg px-4 py-2.5 sm:col-span-2">
              <dt class="label">
                Folder
              </dt>
              <dd class="mono mt-0.5">
                {{ d.spec.static.dir }}/ <span class="font-sans text-fg-muted">— served by the proxy, no container<template v-if="d.spec.static.fallback">; paths that name no file get <span class="mono text-fg">{{ d.spec.static.fallback }}</span></template></span>
              </dd>
            </div>
            <template v-else>
              <div class="bg-bg px-4 py-2.5">
                <dt class="label">
                  Replicas
                </dt>
                <dd class="mono mt-0.5">
                  {{ d.spec.replicas }}
                </dd>
              </div>
              <div class="bg-bg px-4 py-2.5">
                <dt class="label">
                  Port
                </dt>
                <dd class="mono mt-0.5">
                  {{ d.spec.port ?? '—' }}
                </dd>
              </div>
            </template>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                {{ hostnames.length > 1 ? 'Hostnames' : 'Domain' }}
              </dt>
              <dd class="mono mt-0.5 break-all">
                <template v-if="hostnames.length > 0">
                  <div v-for="host in hostnames" :key="host">{{ host }}</div>
                </template>
                <template v-else>
                  —
                </template>
              </dd>
            </div>
            <template v-if="!d.spec.static">
              <div class="bg-bg px-4 py-2.5">
                <dt class="label">
                  Restart · strategy
                </dt>
                <dd class="mono mt-0.5">
                  {{ d.spec.restart.policy }} · {{ d.spec.deploy.strategy }}
                  <span v-if="d.spec.deploy.stop_timeout" class="block text-fg-muted">stop timeout {{ tidyDuration(d.spec.deploy.stop_timeout) }}</span>
                </dd>
              </div>
              <div class="bg-bg px-4 py-2.5 sm:col-span-2">
                <dt class="label">
                  Health check
                </dt>
                <dd class="mono mt-0.5">
                  <template v-if="healthCheck">
                    {{ healthCheck.check }} <span class="text-fg-muted">{{ healthCheck.schedule }}</span>
                  </template>
                  <template v-else>
                    None
                  </template>
                </dd>
              </div>
              <div class="bg-bg px-4 py-2.5 sm:col-span-2">
                <dt class="label">
                  Limits per replica
                </dt>
                <dd class="mono mt-0.5">
                  CPU {{ formatCores(d.spec.resources.cpu) }} · memory {{ d.spec.resources.memory_bytes ? formatBytes(d.spec.resources.memory_bytes) : 'unlimited' }}
                </dd>
              </div>
            </template>
            <div v-if="published.length > 0" class="bg-bg px-4 py-2.5 sm:col-span-2">
              <dt class="label">
                Published ports
              </dt>
              <dd class="mono mt-0.5">
                <div v-for="line in published" :key="line">{{ line }}</div>
              </dd>
            </div>
            <div v-if="proxyLines.length > 0" class="bg-bg px-4 py-2.5 sm:col-span-2">
              <dt class="label">
                Proxy <span class="normal-case tracking-normal text-fg-subtle">passwords are never returned by the agent</span>
              </dt>
              <dd class="mt-0.5 space-y-0.5">
                <div v-for="line in proxyLines" :key="`${line.label}/${line.value}`" class="flex gap-2">
                  <span class="w-20 shrink-0 text-fg-muted">{{ line.label }}</span>
                  <span class="mono min-w-0 break-all">{{ line.value }}</span>
                </div>
              </dd>
            </div>
            <div v-if="d.spec.backups" class="bg-bg px-4 py-2.5 sm:col-span-2">
              <dt class="label">
                Backups
              </dt>
              <dd class="mt-0.5 break-words">
                {{ describeBackupPlan(d.spec.backups) }}
              </dd>
            </div>
            <div v-if="d.spec.volumes?.length" class="bg-bg px-4 py-2.5 sm:col-span-2">
              <dt class="label">
                Volumes
              </dt>
              <dd class="mono mt-0.5">
                <div v-for="v in d.spec.volumes" :key="v.name">{{ v.name }} <span class="text-fg-muted">at</span> {{ v.path }}</div>
              </dd>
            </div>
            <div v-if="process.length > 0" class="bg-bg px-4 py-2.5 sm:col-span-2">
              <dt class="label">
                Process
              </dt>
              <dd class="mt-0.5 space-y-0.5">
                <div v-for="line in process" :key="line.label" class="flex gap-2">
                  <span class="w-20 shrink-0 text-fg-muted">{{ line.label }}</span>
                  <span class="mono min-w-0 break-all">{{ line.value }}</span>
                </div>
              </dd>
            </div>
            <div v-if="d.spec.pre_deploy" class="bg-bg px-4 py-2.5 sm:col-span-2">
              <dt class="label">
                Pre-deploy
              </dt>
              <dd class="mono mt-0.5 break-all">
                {{ formatArgv(d.spec.pre_deploy.command) }} <span class="text-fg-muted">timeout {{ tidyDuration(d.spec.pre_deploy.timeout) }}</span>
              </dd>
            </div>
            <div v-if="d.spec.jobs?.length" class="bg-bg px-4 py-2.5 sm:col-span-2">
              <dt class="label">
                Jobs <span class="normal-case tracking-normal text-fg-subtle">schedules in UTC</span>
              </dt>
              <dd class="mt-0.5 space-y-0.5">
                <div v-for="job in d.spec.jobs" :key="job.name" class="mono break-all">
                  {{ job.name }} <span class="text-fg-muted">{{ job.schedule }}</span> {{ formatArgv(job.command) }} <span class="text-fg-muted">timeout {{ tidyDuration(job.timeout) }}</span>
                </div>
              </dd>
            </div>
            <div v-if="logging" class="bg-bg px-4 py-2.5 sm:col-span-2">
              <dt class="label">
                Logging
              </dt>
              <dd class="mono mt-0.5">
                {{ logging }}
                <details v-if="loggingOptions.length > 0" class="mt-1 font-sans text-xs">
                  <summary class="cursor-pointer select-none text-fg-subtle hover:text-fg">
                    Options
                  </summary>
                  <dl class="mono mt-1 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-0.5">
                    <template v-for="[key, value] in loggingOptions" :key="key">
                      <dt class="text-fg-muted">{{ key }}</dt>
                      <dd class="break-all">{{ value }}</dd>
                    </template>
                  </dl>
                </details>
              </dd>
            </div>
            <div v-if="!d.spec.static" class="bg-bg px-4 py-2.5 sm:col-span-2 lg:col-span-4">
              <dt class="label">
                Environment <span class="normal-case tracking-normal text-fg-subtle">values are never returned by the agent</span>
              </dt>
              <dd class="mt-1.5">
                <ul v-if="envNames.length > 0" class="grid gap-x-8 gap-y-1 sm:grid-cols-2 lg:grid-cols-3">
                  <li v-for="key in envNames" :key="key" class="mono flex items-baseline justify-between gap-3 border-b border-dotted border-line pb-1">
                    <span class="min-w-0 truncate" :title="key">{{ key }}</span>
                    <span class="select-none text-fg-subtle"><span aria-hidden="true">••••••••</span><span class="sr-only">masked</span></span>
                  </li>
                </ul>
                <span v-else class="mono text-fg-muted">No variables</span>
              </dd>
            </div>
          </dl>
        </UiPanel>

        <UiPanel title="Timeline" :meta="events.length">
          <EmptyState v-if="events.length === 0" title="No events recorded" />
          <EventList v-else :events="events" absolute show-type />
        </UiPanel>
      </div>
    </PageBody>
  </div>
</template>
