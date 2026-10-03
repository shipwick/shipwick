<script setup lang="ts">
import type { Standby } from '~/types/api'
import { AgentError } from '~/utils/agentError'
import { serverTabs } from '~/utils/tabs'
import { isStandby } from '~/utils/transfer'

/**
 * The frame of a server's page: which server, whether it answers, and the
 * tabs. With several servers this is the page of the one the address names;
 * the choice between them is in the sidebar and on the list (ServerList).
 */

const polling = useServerInfo()
const agent = useAgent()
const session = useSession()

// Only to mark the tab and the status page of a server that holds applications for a promotion.
const standby = usePolling<Standby | null>(async (signal) => {
  try {
    return await agent.get<Standby>('/standby', { signal })
  }
  catch (cause) {
    // An agent from before there were standbys: it is not one.
    if (cause instanceof AgentError && cause.code === 'ENDPOINT_NOT_FOUND') return null
    throw cause
  }
}, { interval: 60_000 })

const waiting = computed(() => (isStandby(standby.data.value) ? standby.data.value?.applications.length ?? 0 : 0))
provide(STANDBY_WAITING, waiting)

const tabs = computed(() => serverTabs({ waiting: waiting.value }))
</script>

<template>
  <div>
    <PageHeader :crumbs="[{ label: 'Server' }]">
      <template v-if="session.multiple.value" #actions>
        <a href="/servers" class="link text-xs text-fg-muted">All servers</a>
      </template>
    </PageHeader>
    <PageBody>
      <div v-if="polling.loading.value && !polling.data.value" class="space-y-3" aria-busy="true" aria-label="Loading">
        <span class="skeleton h-6 w-48" />
        <span class="skeleton w-72" />
        <span class="skeleton w-56" />
      </div>

      <div v-else-if="polling.error.value && !polling.data.value" class="rounded-sm border border-line">
        <ErrorState :error="polling.error.value" subject="the server" :retrying="polling.refreshing.value" @retry="polling.refresh()" />
      </div>

      <div v-else-if="polling.data.value" class="space-y-6">
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
          <p class="mono truncate text-xl font-semibold">
            {{ polling.data.value.hostname }}
          </p>
          <span v-if="session.multiple.value" class="text-fg-muted">Server <span class="mono text-fg">{{ session.selected.value }}</span></span>
          <StatusBadge :tone="polling.error.value ? 'danger' : 'ok'" :label="polling.error.value ? 'Not responding' : 'Connected'" size="md" />
          <span class="text-fg-muted">Agent <span class="mono text-fg">{{ polling.data.value.agent_version }}</span></span>
        </div>

        <UiTabs :tabs="tabs" label="Sections of the server" />

        <StaleNotice :error="polling.error.value" :updated-at="polling.updatedAt.value" class="!mb-0" />

        <NuxtPage />
      </div>
    </PageBody>
  </div>
</template>
