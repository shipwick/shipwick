<script setup lang="ts">
import type { Container, LogArchiveEntry } from '~/types/api'
import { LIVE_METRICS_SAMPLES } from '~/composables/useLiveMetrics'
import { archiveSupported, endedBecause, hasDied, lastOutputEntry, logsPath } from '~/utils/logArchive'
import { alertsFor } from '~/utils/alerts'
import { describeIssued, hostnameCertificateDisplay } from '~/utils/certificates'
import { diagnose } from '~/utils/diagnosis'
import { formatBytes, formatMemoryUsage, formatPercent, splitImage } from '~/utils/format'
import { DAEMON_USAGE_NOTICE, isUnenforced, unenforcedLimits, unenforcedOf, usageIsTheDaemons } from '~/utils/limits'
import { describeBuild, describeStatic, formatPublish, hostnamesOf } from '~/utils/spec'
import { DRAINING_DISPLAY, containerStateDisplay, isDraining, replicaHealthDisplay } from '~/utils/status'
import { applicationPath } from '~/utils/tabs'

/** The first tab: what is wrong and what to do, then the state of every replica and what happened last. */
const { name, detail, spec, deployments, events, gone, hasActive, isStatic, progress, dismissProgress } = useApplication()

const server = useServerInfo()
const metrics = useLiveMetrics(name, () => hasActive.value && !gone.value && !isStatic.value)

// --- limits Docker does not apply ----------------------------------------------

// The agent's word, from the sample or from the server; an agent before 0.8
// says nothing, and then nothing is marked. A limit that is not enforced is
// still shown, as what deploy.yaml asks for, and usage is not drawn against it.
const unenforced = computed(() => unenforcedLimits(metrics.latest.value, server.data.value?.docker))
const cpuUnenforced = computed(() => isUnenforced(unenforced.value, 'cpu'))
const memoryUnenforced = computed(() => isUnenforced(unenforced.value, 'memory'))
/** Without any cgroup the daemon reports its own usage for every container: not a number to show as a replica's. */
const daemonUsage = computed(() => usageIsTheDaemons(unenforced.value))

// --- what needs attention -----------------------------------------------------

/** A domain is only served if the agent has a reverse proxy, and can reach it. */
const proxyProblem = computed(() => {
  const proxy = server.data.value?.proxy
  if (!proxy) return ''
  if (!proxy.enabled) return 'No reverse proxy is configured on the agent'
  if (!proxy.reachable) return 'The reverse proxy is unreachable, so routing may be stale'
  return ''
})

/** The alerts the agent holds about this application; the server page has them all. */
const alerts = computed(() => alertsFor(server.data.value?.alerts, name.value))
// The deployment the panel below narrates is not also listed as a finding.
const findings = computed(() => diagnose(detail.value, {
  deployments: deployments.data.value ?? [],
  alerts: alerts.value,
  proxyProblem: proxyProblem.value,
  unenforcedLimits: unenforcedOf(unenforced.value, { memory: spec.value?.resources.memory_bytes ?? 0, cpu: spec.value?.resources.cpu ?? 0 }),
  usageIsTheDaemons: daemonUsage.value,
}).filter(finding => finding.key !== `attempt-${progress.value?.deploymentId}`))

// --- identity -----------------------------------------------------------------

/** "42 files, 3.1 MB, served by the proxy": what the active static deployment serves. */
const servedFiles = computed(() => describeStatic(detail.value.active_deployment?.static))
/** Where a `build` application's image comes from; "" for an image pulled from a registry. */
const buildOrigin = computed(() => describeBuild(spec.value?.build))
const published = computed(() => (spec.value?.publish ?? []).map(formatPublish))

// Every hostname the application answers on. Redirects are answered by the
// proxy itself, so they keep working while the application is stopped.
const hostnames = computed(() => hostnamesOf(detail.value))

// The certificate the proxy presents for each hostname. Only what is not in
// order gets a badge; issuer and expiry are on the hostname's tooltip.
const certificateOf = computed(() => new Map((detail.value.certificates ?? []).map(c => [c.hostname, c])))
function certificateBadge(host: string) {
  const certificate = certificateOf.value.get(host)
  return certificate ? hostnameCertificateDisplay(certificate) : null
}
function certificateTitle(host: string): string | undefined {
  const certificate = certificateOf.value.get(host)
  const issued = certificate ? describeIssued(certificate) : ''
  return issued ? `Certificate: ${issued}` : undefined
}

// --- live numbers -------------------------------------------------------------

const cpuValues = computed(() => metrics.samples.value.map(s => s.cpu_percent))
const memoryValues = computed(() => metrics.samples.value.map(s => s.memory_bytes))
const sampleTimes = computed(() => metrics.samples.value.map(s => s.collected_at))

// Limits come with the sample (percent of one core, summed over the replicas
// that were measured; 0 = unlimited), so they stay right while a rollout
// changes the number of containers.
const cpuLimit = computed(() => metrics.latest.value?.cpu_limit_percent || null)
const memoryLimit = computed(() => metrics.latest.value?.memory_limit_bytes || null)
const cpuCeiling = computed(() => (cpuUnenforced.value ? null : cpuLimit.value))
const memoryCeiling = computed(() => (memoryUnenforced.value ? null : memoryLimit.value))

function replicaMetrics(container: Container) {
  return metrics.latest.value?.replicas.find(r => r.container === container.name) ?? null
}

// --- replicas -----------------------------------------------------------------

/**
 * A deployment is complete once the new replicas serve; a container it
 * replaced may still be using up its stop_timeout. It is listed, but as what
 * it is: on its way out, not one more replica.
 */
const draining = (c: Container) => isDraining(c, detail.value)

/**
 * By replica, the older deployment's container first: a stable order while a
 * rollout swaps them. What is on its way out comes after the replicas, apart
 * from them.
 */
const containers = computed(() => [...detail.value.containers].sort((a, b) =>
  Number(draining(a)) - Number(draining(b)) || a.replica - b.replica || a.deployment_id - b.deployment_id))

/** During a rollout the replicas of two deployments run side by side; say which is which. */
const mixedVersions = computed(() => new Set(detail.value.containers.map(c => c.deployment_id)).size > 1)
const sequenceById = computed(() => new Map((deployments.data.value ?? []).map(d => [d.id, d.sequence])))

const replicaCount = computed(() => detail.value.containers.filter(c => !draining(c)).length)
const drainingCount = computed(() => detail.value.containers.length - replicaCount.value)

// A replica that restarted or lies dead said why before it ended, and the
// agent kept that: the row links to it. Asked only while such a replica is
// listed, and only of an agent that keeps output.
const agent = useAgent()
const someDied = computed(() => !isStatic.value && archiveSupported(server.data.value) && detail.value.containers.some(c => !draining(c) && hasDied(c)))
const kept = usePolling<LogArchiveEntry[]>(
  signal => agent.get<LogArchiveEntry[]>(`/applications/${encodeURIComponent(name.value)}/logs/archive`, { query: { kind: 'replica', limit: 50 }, signal }),
  { interval: 15_000, enabled: () => someDied.value },
)
function lastOutput(c: Container): LogArchiveEntry | null {
  return someDied.value && !draining(c) && hasDied(c) ? lastOutputEntry(kept.data.value ?? [], c.name) : null
}

// --- what happened last -------------------------------------------------------

const RECENT = 5
const recentDeployments = computed(() => (deployments.data.value ?? []).slice(0, RECENT))
</script>

<template>
  <div class="space-y-8">
    <div v-if="alerts.length > 0 || findings.length > 0" class="space-y-2">
      <AlertList :alerts="alerts" :link-application="false" />
      <FindingList :findings="findings" />
    </div>

    <DeploymentProgressPanel
      v-if="progress"
      :progress="progress"
      :still-running="detail.version"
      :app-status="detail.status"
      @dismiss="dismissProgress"
    />

    <!-- What it is on the left, live numbers on the right -->
    <div class="grid gap-6 lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
      <dl class="grid min-w-0 grid-cols-[5.5rem_minmax(0,1fr)] content-start gap-x-3 gap-y-2">
        <template v-if="isStatic">
          <dt class="label pt-0.5">
            Files
          </dt>
          <dd>
            {{ servedFiles || 'Nothing is served yet' }}
          </dd>
        </template>
        <template v-else>
          <dt class="label pt-0.5">
            Image
          </dt>
          <dd class="mono [overflow-wrap:anywhere]">
            {{ detail.image || '—' }}
            <UiTooltip v-if="buildOrigin" class="block font-sans text-fg-muted" text="The agent never builds: a new image comes from running shipwick deploy in the project">{{ buildOrigin }}</UiTooltip>
          </dd>
        </template>
        <dt class="label pt-0.5">
          {{ hostnames.length > 1 ? 'Addresses' : 'Address' }}
        </dt>
        <dd>
          <template v-if="hostnames.length > 0">
            <div v-for="entry in hostnames" :key="entry.host" class="flex flex-wrap items-center gap-x-2">
              <UiTooltip
                v-if="entry.url"
                as="a"
                :href="entry.url"
                target="_blank"
                rel="noopener noreferrer"
                class="mono link inline-flex min-w-0 items-center gap-1 break-all"
                :class="entry.kind === 'redirect' ? 'text-fg-muted' : ''"
                :text="certificateTitle(entry.host)"
              >{{ entry.address }}<UiIcon name="external" :size="12" /><span class="sr-only">(opens in a new tab)</span></UiTooltip>
              <!-- A wildcard is a pattern, not an address: there is nothing to open. -->
              <UiTooltip v-else class="mono break-all" :text="certificateTitle(entry.host) ?? 'Every name one label below is served alike'">{{ entry.address }}</UiTooltip>
              <UiTooltip v-if="entry.kind === 'redirect'" class="mono text-fg-muted" :text="`Answered with a redirect to https://${entry.target}, also while the application is stopped`">→ {{ entry.target }}</UiTooltip>
              <span v-else-if="entry.kind === 'alias'" class="text-xs text-fg-subtle">alias</span>
              <StatusBadge v-if="certificateBadge(entry.host)" v-bind="certificateBadge(entry.host)!" :detail="certificateOf.get(entry.host)?.message || undefined" :raw="certificateOf.get(entry.host)?.status" />
            </div>
          </template>
          <span v-else class="text-fg-muted">No domain: nothing reaches it through the proxy</span>
        </dd>
        <template v-if="published.length > 0">
          <dt class="label pt-0.5">
            <UiTooltip text="Reachable from outside the reverse proxy, on the server's own port">Published</UiTooltip>
          </dt>
          <dd class="mono">
            <div v-for="line in published" :key="line">
              {{ line }}
            </div>
          </dd>
        </template>
      </dl>

      <!-- Where the live numbers would be: a static application has none to show. -->
      <div v-if="isStatic" class="min-w-0 self-start rounded-sm border border-line px-4 py-3 text-fg-muted">
        This application is a folder served by the proxy; it has no containers. There are no replicas, logs or metrics, and nothing to run a command in.
        To serve other files, run <span class="mono text-fg">shipwick deploy</span> from the project.
      </div>
      <div v-else class="min-w-0 self-start divide-y divide-line rounded-sm border border-line">
        <div class="grid grid-cols-[4.5rem_minmax(0,1fr)] items-center gap-x-4 px-4 py-2.5 sm:grid-cols-[4.5rem_11rem_minmax(0,1fr)]">
          <span class="label">CPU</span>
          <span class="mono">
            {{ daemonUsage ? '—' : formatPercent(metrics.latest.value?.cpu_percent) }}
            <span v-if="metrics.latest.value && cpuLimit" class="text-fg-subtle">/ {{ formatPercent(cpuLimit) }}</span>
            <span v-if="metrics.latest.value && cpuLimit && cpuUnenforced" class="block font-sans text-xs text-warn">limit not enforced</span>
          </span>
          <span v-if="daemonUsage" class="text-xs text-fg-muted max-sm:col-span-2">{{ DAEMON_USAGE_NOTICE }}</span>
          <Sparkline
            v-else-if="metrics.availability.value === 'available'"
            class="max-sm:col-span-2 max-sm:mt-1.5"
            label="CPU"
            :values="cpuValues"
            :times="sampleTimes"
            :slots="LIVE_METRICS_SAMPLES"
            :ceiling="cpuCeiling"
            :format="formatPercent"
          />
          <span v-else class="text-xs text-fg-subtle max-sm:col-span-2">{{ metrics.availability.value === 'loading' ? '' : metrics.reason.value }}</span>
        </div>
        <div class="grid grid-cols-[4.5rem_minmax(0,1fr)] items-center gap-x-4 px-4 py-2.5 sm:grid-cols-[4.5rem_11rem_minmax(0,1fr)]">
          <span class="label">Memory</span>
          <span class="mono">
            <template v-if="daemonUsage">
              — <span v-if="memoryLimit" class="text-fg-subtle">/ {{ formatBytes(memoryLimit) }}</span>
            </template>
            <template v-else>
              {{ formatMemoryUsage(metrics.latest.value?.memory_bytes, metrics.latest.value?.memory_limit_bytes) }}
            </template>
            <span v-if="metrics.latest.value && memoryLimit && memoryUnenforced" class="block font-sans text-xs text-warn">limit not enforced</span>
          </span>
          <Sparkline
            v-if="metrics.availability.value === 'available' && !daemonUsage"
            class="max-sm:col-span-2 max-sm:mt-1.5"
            label="Memory"
            :values="memoryValues"
            :times="sampleTimes"
            :slots="LIVE_METRICS_SAMPLES"
            :ceiling="memoryCeiling"
            :format="formatBytes"
          />
        </div>
        <div class="flex items-center justify-between gap-x-4 px-4 py-2">
          <span class="text-xs text-fg-subtle">Live, since this page was opened</span>
          <NuxtLink :to="applicationPath(name, 'metrics')" class="link text-xs text-fg-muted">
            History and traffic
          </NuxtLink>
        </div>
      </div>
    </div>

    <UiPanel v-if="!isStatic" title="Replicas" :meta="drainingCount > 0 ? `${replicaCount} · ${drainingCount} stopping` : replicaCount">
      <EmptyState v-if="detail.containers.length === 0" title="No containers">
        <template v-if="detail.status === 'STOPPED'">
          The application is stopped. Start it to run its replicas again.
        </template>
        <template v-else>
          Nothing is running for this application.
        </template>
      </EmptyState>
      <div v-else class="overflow-x-auto">
        <table class="data-table stack" aria-label="Replicas">
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
            <tr v-for="c in containers" :key="c.id" :class="draining(c) ? 'text-fg-muted' : ''">
              <td data-primary class="mono">
                <span :class="draining(c) ? 'line-through decoration-fg-faint' : ''">{{ c.replica }}</span><span class="ml-2 text-fg-muted rows:hidden">{{ c.name }}</span>
              </td>
              <td class="mono cards:!hidden">
                <UiTooltip :text="`${c.id.slice(0, 12)} · ${c.ip || 'no address'}`">{{ c.name }}</UiTooltip>
                <UiTooltip v-if="mixedVersions" repeats :text="c.image" class="ml-2 text-fg-subtle">
                  {{ splitImage(c.image).tag || 'latest' }}<template v-if="sequenceById.get(c.deployment_id)"> · #{{ sequenceById.get(c.deployment_id) }}</template>
                </UiTooltip>
              </td>
              <td data-label="State">
                <StatusBadge
                  v-if="draining(c)"
                  v-bind="DRAINING_DISPLAY"
                  detail="On its way out: it was asked to stop and has its stop_timeout to exit. It is no longer a replica."
                />
                <StatusBadge v-else v-bind="containerStateDisplay(c)" :raw="c.state" />
              </td>
              <td data-label="Health">
                <span v-if="draining(c)" class="text-fg-subtle">—</span>
                <StatusBadge v-else v-bind="replicaHealthDisplay(c.health)" :raw="c.health || 'no health check configured'" />
              </td>
              <td data-label="Restarts" class="mono" :class="draining(c) ? '' : c.crash_loop ? 'text-danger' : c.restarts > 0 ? 'text-warn' : 'text-fg-muted'">
                <span v-if="draining(c)" class="text-fg-subtle">—</span>
                <span v-else>{{ c.restarts }}<span v-if="c.crash_loop" class="font-sans"> (keeps crashing)</span></span>
                <UiTooltip
                  v-if="lastOutput(c)"
                  :to="logsPath(name, { view: 'archive', entry: lastOutput(c)!.id })"
                  class="link ml-2 whitespace-nowrap font-sans text-xs text-fg-muted"
                  :text="`Replica ${c.replica} ${endedBecause(lastOutput(c)!)}: what it wrote before that`"
                  :aria-label="`Last output of replica ${c.replica}`"
                >last output</UiTooltip>
              </td>
              <td data-label="CPU" class="mono right text-fg-muted">
                <template v-if="daemonUsage">
                  —
                </template>
                <template v-else>
                  {{ formatPercent(replicaMetrics(c)?.cpu_percent) }}<template v-if="replicaMetrics(c)?.cpu_limit_percent && !cpuUnenforced"> / {{ formatPercent(replicaMetrics(c)?.cpu_limit_percent) }}</template>
                </template>
              </td>
              <td data-label="Memory" class="mono right text-fg-muted">
                {{ daemonUsage ? '—' : formatMemoryUsage(replicaMetrics(c)?.memory_bytes, memoryUnenforced ? 0 : replicaMetrics(c)?.memory_limit_bytes) }}
              </td>
              <td data-label="Started" class="right text-fg-muted">
                <TimeAgo :time="c.started_at" />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </UiPanel>

    <UiPanel title="Latest deployments">
      <template #actions>
        <NuxtLink :to="applicationPath(name, 'deployments')" class="link text-xs text-fg-muted">
          All deployments
        </NuxtLink>
      </template>
      <TableSkeleton v-if="deployments.loading.value" :rows="3" :columns="5" />
      <ErrorState
        v-else-if="deployments.error.value && !deployments.data.value"
        :error="deployments.error.value"
        subject="deployments"
        :retrying="deployments.refreshing.value"
        @retry="deployments.refresh()"
      />
      <EmptyState v-else-if="recentDeployments.length === 0" title="No deployments" />
      <div v-else class="overflow-x-auto">
        <DeploymentsTable :deployments="recentDeployments" :known="deployments.data.value ?? []" :show-application="false" />
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
      <div v-else class="max-h-80 overflow-y-auto focus-visible:-outline-offset-2" tabindex="0" role="group" aria-label="Events">
        <EventList :events="events.data.value ?? []" />
      </div>
    </UiPanel>
  </div>
</template>
