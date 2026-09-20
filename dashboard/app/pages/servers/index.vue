<script setup lang="ts">
import type { Server } from '~/types/api'
import { formatBytes, pluralize } from '~/utils/format'
import type { Tone } from '~/utils/status'

useHead({ title: 'Servers' })

const polling = useServerInfo()

/**
 * An agent manages exactly one server, so this list has exactly one entry.
 * It is a list all the same: when the dashboard learns to talk to several
 * agents, more entries appear here and nothing else on this page changes.
 */
interface ServerEntry {
  id: string
  server: Server
}

const servers = computed<ServerEntry[]>(() => (polling.data.value ? [{ id: polling.data.value.hostname, server: polling.data.value }] : []))

function proxyDisplay(server: Server): { tone: Tone, label: string, detail: string } {
  const p = server.proxy
  if (!p.enabled) return { tone: 'muted', label: 'Disabled', detail: 'Domains are not routed by this agent.' }
  if (!p.reachable) return { tone: 'danger', label: 'Unreachable', detail: p.error || 'The agent cannot reach the Caddy admin API.' }
  return { tone: 'ok', label: 'Running', detail: `${pluralize(p.routes, 'route')} configured` }
}
</script>

<template>
  <div>
    <PageHeader :crumbs="[{ label: 'Servers' }]" />
    <PageBody>
      <StaleNotice :error="polling.data.value ? polling.error.value : null" :updated-at="polling.updatedAt.value" />

      <div v-if="polling.loading.value && !polling.data.value" class="rounded-sm border border-line p-4" aria-busy="true" aria-label="Loading">
        <span class="skeleton h-5 w-48" />
        <span class="skeleton mt-3 w-72" />
        <span class="skeleton mt-2 w-56" />
      </div>

      <div v-else-if="polling.error.value && !polling.data.value" class="rounded-sm border border-line">
        <ErrorState :error="polling.error.value" subject="the server" :retrying="polling.refreshing.value" @retry="polling.refresh()" />
      </div>

      <ul v-else class="space-y-6">
        <li v-for="entry in servers" :key="entry.id" class="overflow-hidden rounded-sm border border-line">
          <header class="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-b border-line bg-subtle px-4 py-2.5">
            <div class="flex min-w-0 items-center gap-3">
              <h2 class="mono truncate text-base font-semibold">
                {{ entry.server.hostname }}
              </h2>
              <StatusBadge :tone="polling.error.value ? 'danger' : 'ok'" :label="polling.error.value ? 'Not responding' : 'Connected'" />
            </div>
            <span class="text-xs text-fg-muted">Agent <span class="mono text-fg">{{ entry.server.agent_version }}</span></span>
          </header>

          <dl class="grid gap-px bg-line sm:grid-cols-2 lg:grid-cols-4">
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Operating system
              </dt>
              <dd class="mt-0.5">
                {{ entry.server.os || '—' }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Kernel
              </dt>
              <dd class="mono mt-0.5 break-all">
                {{ entry.server.kernel || '—' }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Architecture
              </dt>
              <dd class="mono mt-0.5">
                {{ entry.server.architecture || '—' }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Docker
              </dt>
              <dd class="mono mt-0.5">
                {{ entry.server.docker_version || '—' }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                CPUs
              </dt>
              <dd class="mono mt-0.5">
                {{ entry.server.cpus }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Memory
              </dt>
              <dd class="mono mt-0.5">
                {{ formatBytes(entry.server.memory_bytes) }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Applications
              </dt>
              <dd class="mt-0.5">
                <NuxtLink to="/applications" class="mono link">{{ entry.server.applications }}</NuxtLink>
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5">
              <dt class="label">
                Running containers
              </dt>
              <dd class="mono mt-0.5">
                {{ entry.server.containers }}
              </dd>
            </div>
            <div class="bg-bg px-4 py-2.5 sm:col-span-2 lg:col-span-4">
              <dt class="label">
                Reverse proxy (Caddy)
              </dt>
              <dd class="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5">
                <StatusBadge :tone="proxyDisplay(entry.server).tone" :label="proxyDisplay(entry.server).label" />
                <span class="text-fg-muted">{{ proxyDisplay(entry.server).detail }}</span>
              </dd>
            </div>
          </dl>
        </li>
      </ul>
    </PageBody>
  </div>
</template>
