<script setup lang="ts">
import type { BackupRun, BackupStatus } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { durationBetween, formatBytes, formatDuration } from '~/utils/format'
import { backupBusy, backupSize, backupStatusDisplay, backupUsable, describeDestination, describeDestinations, stateBackupDisplay } from '~/utils/backups'
import { roleHint } from '~/utils/roles'

/**
 * Where backups go, and how the agent's own state — its database and the key
 * that encrypts the secrets in it — is backed up. The summary comes with
 * GET /server and every role sees it; the list of state backups and the
 * button that takes one are admin's.
 */
const props = defineProps<{
  status: BackupStatus
  admin: boolean
}>()

const emit = defineEmits<{
  /** A state backup finished: GET /server has a new `state_last_at`. */
  changed: []
}>()

const agent = useAgent()
const now = useNow()

const display = computed(() => stateBackupDisplay(props.status, now.value))

const anyBusy = ref(false)
const runs = usePolling<BackupRun[]>(signal => agent.get<BackupRun[]>('/server/backups', { query: { limit: 10 }, signal }), {
  interval: () => (anyBusy.value ? 2000 : 60_000),
  enabled: () => props.admin,
})
watch(runs.data, (list) => {
  const was = anyBusy.value
  anyBusy.value = (list ?? []).some(backupBusy)
  if (was && !anyBusy.value) emit('changed')
})

const starting = ref(false)
const startError = shallowRef<AgentError | null>(null)

async function backUpNow() {
  if (starting.value) return
  starting.value = true
  startError.value = null
  try {
    const run = await agent.post<BackupRun>('/server/backups')
    runs.data.value = [run, ...(runs.data.value ?? []).filter(r => r.id !== run.id)]
    anyBusy.value = true
  }
  catch (cause) {
    startError.value = toAgentError(cause)
  }
  finally {
    starting.value = false
  }
}

const startTitle = computed(() => {
  if (!props.admin) return roleHint('admin')
  if (!display.value.possible) return 'Needs SHIPWICK_BACKUP_PASSPHRASE: the key is never written anywhere unencrypted'
  if (anyBusy.value) return 'A backup of the state is in progress'
  return undefined
})

const duration = (r: BackupRun) => (r.completed_at ? formatDuration(durationBetween(r.started_at, r.completed_at)) : '…')
</script>

<template>
  <UiPanel title="Backups">
    <template #actions>
      <UiButton size="sm" :disabled="!props.admin || !display.possible || anyBusy" :pending="starting" :title="startTitle" @click="backUpNow">
        <UiIcon name="download" :size="12" />
        Back up state now
      </UiButton>
    </template>

    <dl class="grid gap-px bg-line sm:grid-cols-2 lg:grid-cols-4">
      <div class="bg-bg px-4 py-2.5">
        <dt class="label">
          Kept in
        </dt>
        <dd class="mt-0.5">
          {{ describeDestination(props.status.destination) }}
        </dd>
      </div>
      <div class="bg-bg px-4 py-2.5">
        <dt class="label">
          Encryption
        </dt>
        <dd class="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5">
          <StatusBadge :tone="props.status.encrypted ? 'ok' : 'muted'" :label="props.status.encrypted ? 'With a passphrase' : 'None'" />
        </dd>
      </div>
      <div class="bg-bg px-4 py-2.5 sm:col-span-2">
        <dt class="label">
          The agent's own state
        </dt>
        <dd class="mt-0.5 flex flex-wrap items-baseline gap-x-3 gap-y-0.5">
          <StatusBadge :tone="display.tone" :label="display.label" :raw="props.status.state_last_at ?? undefined" />
          <span class="min-w-0 break-words" :class="display.tone === 'danger' ? 'text-danger' : display.tone === 'warn' ? 'text-warn' : 'text-fg-muted'">{{ display.detail }}</span>
        </dd>
      </div>
    </dl>

    <InlineError :error="startError" class="m-3" />

    <template v-if="props.admin && (runs.data.value?.length ?? 0) > 0">
      <div class="overflow-x-auto border-t border-line">
        <table class="data-table stack">
          <thead>
            <tr>
              <th class="w-16">
                Backup
              </th>
              <th>Taken</th>
              <th>Started by</th>
              <th class="right">
                Size
              </th>
              <th>Kept in</th>
              <th>Status</th>
              <th class="right">
                Took
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in runs.data.value" :key="r.id">
              <td data-primary class="mono">
                #{{ r.id }}
              </td>
              <td data-label="Taken" class="text-fg-muted">
                <TimeAgo :time="r.started_at" />
              </td>
              <td data-label="Started by" class="text-fg-muted">
                {{ r.trigger === 'schedule' ? 'daily schedule' : 'by hand' }}
              </td>
              <td data-label="Size" class="mono right text-fg-muted" :title="r.volumes.map(v => `${v.volume} ${formatBytes(v.size_bytes)}`).join(' · ') || undefined">
                {{ backupUsable(r) ? formatBytes(backupSize(r)) : '—' }}
              </td>
              <td data-label="Kept in" class="mono text-fg-muted">
                {{ describeDestinations(r) }}
              </td>
              <td data-label="Status">
                <StatusBadge v-bind="backupStatusDisplay(r)" :raw="r.status" />
                <span v-if="r.status === 'failed' && r.error" class="block max-w-sm truncate text-xs text-danger" :title="r.error">{{ r.error }}</span>
              </td>
              <td data-label="Took" class="mono right text-fg-muted">
                {{ duration(r) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
    <p class="border-t border-line px-4 py-2 text-xs text-fg-subtle">
      The state is the database and the key that encrypts the secrets in it: taken daily, seven kept, only ever encrypted. It is restored with the agent stopped; the handbook says how under <span class="text-fg-muted">Restoring the agent's state</span>. Backups of an application's volumes are on its own page.
    </p>
  </UiPanel>
</template>
