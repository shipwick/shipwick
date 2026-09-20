<script setup lang="ts">
import type { Deployment, DeploymentDetail } from '~/types/api'
import { deriveProgress } from '~/utils/deploymentProgress'
import { durationBetween, formatAbsoluteUtc, formatBytes, formatCores, formatDuration } from '~/utils/format'
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
    case 'FAILED': return 'Failed before any replica of the previous version was replaced; nothing was affected'
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

      <div v-else-if="deployment.loading.value && !d" class="space-y-3" aria-busy="true" aria-label="Loading">
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
          <p v-if="d.error" class="mt-3 flex items-start gap-2 rounded-sm border border-danger-line bg-danger-bg px-3 py-2 font-medium text-danger">
            <UiIcon name="x-circle" :size="14" class="mt-[3px]" />
            <span class="min-w-0 break-words">{{ d.error }}</span>
          </p>
        </div>

        <!-- Metadata -->
        <UiPanel title="Details">
          <dl class="grid gap-px bg-line sm:grid-cols-2 lg:grid-cols-4">
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Version
              </dt>
              <dd class="mono mt-0.5">
                {{ d.version || '—' }}
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
            <div class="bg-bg px-4 py-2.5 sm:col-span-2 lg:col-span-3">
              <dt class="label">
                Image
              </dt>
              <dd class="mono mt-0.5 break-all">
                {{ d.image }}
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
          <dl class="grid gap-px bg-line sm:grid-cols-2 lg:grid-cols-4">
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
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Domain
              </dt>
              <dd class="mono mt-0.5 break-all">
                {{ d.spec.domain || '—' }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Restart · strategy
              </dt>
              <dd class="mono mt-0.5">
                {{ d.spec.restart.policy }} · {{ d.spec.deploy.strategy }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5 sm:col-span-2">
              <dt class="label">
                Health check
              </dt>
              <dd class="mono mt-0.5">
                <template v-if="d.spec.health">
                  GET {{ d.spec.health.path }} every {{ d.spec.health.interval }}, timeout {{ d.spec.health.timeout }}, {{ d.spec.health.retries }} retries
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
            <div class="bg-bg px-4 py-2.5 sm:col-span-2 lg:col-span-4">
              <dt class="label">
                Environment <span class="normal-case tracking-normal text-fg-faint">values are never returned by the agent</span>
              </dt>
              <dd class="mt-1.5">
                <ul v-if="envNames.length > 0" class="grid gap-x-8 gap-y-1 sm:grid-cols-2 lg:grid-cols-3">
                  <li v-for="key in envNames" :key="key" class="mono flex items-baseline justify-between gap-3 border-b border-dotted border-line pb-1">
                    <span class="min-w-0 truncate" :title="key">{{ key }}</span>
                    <span class="select-none text-fg-faint" aria-label="masked">••••••••</span>
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
