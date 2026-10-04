<script setup lang="ts">
import type { Server } from '~/types/api'
import { stateBackupDisplay } from '~/utils/backups'
import { formatBytes, pluralize } from '~/utils/format'
import { describeLogArchive } from '~/utils/logArchive'
import { describeNetwork } from '~/utils/network'
import type { Tone } from '~/utils/status'
import { updateLine, updateNotice } from '~/utils/updates'

/** Whether the server is healthy: what needs attention first, then what it has and what it runs. */
const polling = useServerInfo()
const waiting = useStandbyWaiting()
const now = useNow()

const server = computed(() => polling.data.value as Server)

const proxy = computed<{ tone: Tone, label: string, detail: string }>(() => {
  const p = server.value.proxy
  if (!p.enabled) return { tone: 'muted', label: 'Off', detail: 'Domains are not routed by this agent.' }
  if (!p.reachable) return { tone: 'danger', label: 'Unreachable', detail: p.error || 'The agent cannot reach the Caddy admin API.' }
  return { tone: 'ok', label: 'Running', detail: `${pluralize(p.routes, 'route')} configured` }
})

const stateBackup = computed(() => (server.value.backups ? stateBackupDisplay(server.value.backups, now.value) : null))
/** Only on a server where something was set: a proxy, authorities of its own, resolvers, another certificate authority. */
const network = computed(() => describeNetwork(server.value.network))
/** What the agent heard about newer releases, when there is nothing to do about it: said quietly next to its version. */
const upToDate = computed(() => updateLine(server.value.update, now.value))
const newer = computed(() => updateNotice(server.value))
</script>

<template>
  <div class="space-y-8">
    <p v-if="waiting > 0" class="flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-sm border border-warn-line bg-warn-bg px-3 py-2 text-xs text-warn" role="status">
      <UiIcon name="alert" :size="14" />
      <span class="min-w-0 flex-1 basis-64">
        <span class="font-medium">This server is a standby.</span>
        {{ waiting === 1 ? 'One application waits' : `${waiting} applications wait` }} here stopped, ready to be started when the first server is gone.
      </span>
      <NuxtLink to="/servers/transfer" class="notice-action">
        Standby
        <UiIcon name="chevron-right" :size="12" />
      </NuxtLink>
    </p>

    <UpdateNotice :server="server" />

    <UiPanel v-if="server.alerts !== undefined" title="Alerts" :meta="server.alerts.length" :bordered="false">
      <AlertList v-if="server.alerts.length > 0" :alerts="server.alerts" />
      <p v-else class="flex items-start gap-2 rounded-sm border border-line px-4 py-3 text-fg-muted">
        <UiIcon name="check" :size="14" class="mt-[3px] text-ok" />
        Nothing needs attention: no replica is near its memory limit or being restarted over and over, every application has its healthy replicas, and the disk has room.
      </p>
    </UiPanel>

    <UiPanel title="Resources">
      <dl class="facts sm:grid-cols-2 lg:grid-cols-4">
        <!-- Absent on an agent before 0.5; null where the agent cannot measure it. -->
        <div v-if="server.disk !== undefined" class="sm:col-span-2 lg:col-span-4">
          <dt class="label">
            Disk
          </dt>
          <dd class="mt-0.5">
            <DiskMeter :disk="server.disk ?? null" :alerts="server.alerts ?? []" />
          </dd>
        </div>
        <div>
          <dt class="label">
            CPUs
          </dt>
          <dd class="mono mt-0.5">
            {{ server.cpus }}
          </dd>
        </div>
        <div>
          <dt class="label">
            Memory
          </dt>
          <dd class="mono mt-0.5">
            {{ formatBytes(server.memory_bytes) }}
          </dd>
        </div>
        <div>
          <dt class="label">
            Applications
          </dt>
          <dd class="mt-0.5">
            <NuxtLink to="/applications" class="mono link">{{ server.applications }}</NuxtLink>
          </dd>
        </div>
        <div>
          <dt class="label">
            Running containers
          </dt>
          <dd class="mono mt-0.5">
            {{ server.containers }}
          </dd>
        </div>
      </dl>
    </UiPanel>

    <UiPanel title="Services">
      <dl class="facts sm:grid-cols-2">
        <div>
          <dt class="label">
            Reverse proxy
          </dt>
          <dd class="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5">
            <StatusBadge :tone="proxy.tone" :label="proxy.label" />
            <span class="text-fg-muted">{{ proxy.detail }}</span>
            <span v-if="server.proxy.dns_challenge" class="basis-full text-fg-muted">
              Certificates are obtained through Cloudflare DNS: hostnames may be proxied by Cloudflare and may be wildcards.
            </span>
            <span v-if="server.proxy.plain_lookups" class="basis-full text-fg-muted">
              Not Shipwick's image of this version: a name lookup Docker leaves unanswered holds every request for seconds. On the server, run the installer again; an image of your own is built from <span class="mono text-fg">Dockerfile.caddy</span>.
            </span>
          </dd>
        </div>
        <div v-if="stateBackup">
          <dt class="label">
            Backup of the agent's own data
          </dt>
          <dd class="mt-0.5 flex flex-wrap items-baseline gap-x-3 gap-y-0.5">
            <StatusBadge :tone="stateBackup.tone" :label="stateBackup.label" />
            <NuxtLink to="/servers/backups" class="link text-fg-muted">
              Backups
            </NuxtLink>
          </dd>
        </div>
        <div>
          <dt class="label">
            Notifications
          </dt>
          <dd class="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5">
            <template v-if="server.notifications?.webhook">
              <StatusBadge tone="ok" label="Webhook configured" />
              <span class="text-fg-muted">Deployment outcomes, outages and alerts are posted to it.</span>
            </template>
            <template v-else>
              <StatusBadge tone="muted" label="None" />
              <span class="text-fg-muted">Nobody is told when something breaks. Set <span class="mono text-fg">SHIPWICK_WEBHOOK_URL</span> on the agent.</span>
            </template>
          </dd>
        </div>
        <!-- Where the agent says the dashboard is served: what `shipwick open --dashboard` opens. -->
        <div v-if="server.dashboard_url !== undefined">
          <dt class="label">
            Dashboard address
          </dt>
          <dd class="mt-0.5">
            <span v-if="server.dashboard_url" class="mono break-all">{{ server.dashboard_url }}</span>
            <span v-else class="text-fg-muted">No hostname: set <span class="mono text-fg">SHIPWICK_DASHBOARD_DOMAIN</span> to serve it over HTTPS through the proxy.</span>
          </dd>
        </div>
        <!-- What is kept of containers that ended; absent on an agent before 0.7. -->
        <div v-if="server.log_archive">
          <dt class="label">
            Log archive
          </dt>
          <dd class="mt-0.5">
            <template v-if="server.log_archive.enabled">
              <span class="mono">{{ describeLogArchive(server.log_archive) }}</span>
              <span class="block text-xs text-fg-muted">The last output of every container that ended, under Logs on an application's page.</span>
            </template>
            <template v-else>
              <StatusBadge tone="muted" label="Off" />
              <span class="ml-2 text-fg-muted">Nothing is kept of a container that ended: <span class="mono text-fg">SHIPWICK_LOG_RETENTION_SIZE</span> is 0 on the agent.</span>
            </template>
          </dd>
        </div>
        <div v-if="network" class="sm:col-span-2">
          <dt class="label">
            Network
          </dt>
          <dd class="mt-0.5">
            <span class="mono break-words">{{ network.parts.join(' · ') }}</span>
            <span v-if="network.warning" class="mt-1 flex items-start gap-2 text-warn" role="status">
              <UiIcon name="alert" :size="14" class="mt-[3px]" />
              <span class="min-w-0 break-words">{{ network.warning }}</span>
            </span>
          </dd>
        </div>
      </dl>
    </UiPanel>

    <UiPanel title="System">
      <dl class="facts sm:grid-cols-2 lg:grid-cols-4">
        <div class="sm:col-span-2 lg:col-span-4">
          <dt class="label">
            Shipwick agent
          </dt>
          <dd class="mt-0.5 flex flex-wrap items-baseline gap-x-3 gap-y-0.5">
            <span class="mono">{{ server.agent_version }}</span>
            <a v-if="newer" href="#update" class="link text-fg-muted">{{ newer.latest }} is available</a>
            <span v-else-if="upToDate" class="text-fg-muted">{{ upToDate }}</span>
          </dd>
        </div>
        <div>
          <dt class="label">
            Operating system
          </dt>
          <dd class="mt-0.5">
            {{ server.os || '—' }}
          </dd>
        </div>
        <div>
          <dt class="label">
            Kernel
          </dt>
          <dd class="mono mt-0.5 break-all">
            {{ server.kernel || '—' }}
          </dd>
        </div>
        <div>
          <dt class="label">
            Architecture
          </dt>
          <dd class="mono mt-0.5">
            {{ server.architecture || '—' }}
          </dd>
        </div>
        <div>
          <dt class="label">
            Docker
          </dt>
          <dd class="mono mt-0.5">
            {{ server.docker_version || '—' }}
          </dd>
        </div>
      </dl>
    </UiPanel>
  </div>
</template>
