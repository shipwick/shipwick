<script setup lang="ts">
import type { BackupRun, Import, Promotion, Standby } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { AgentError as AgentFailure, toAgentError } from '~/utils/agentError'
import { durationBetween, formatBytes, formatDuration } from '~/utils/format'
import { backupBusy, backupSize, backupStatusDisplay, backupUsable, describeDestinations, triggerLabel } from '~/utils/backups'
import { roleHint } from '~/utils/roles'
import { PROMOTE_WORD, describePull, importOutcome, importProgress, importStored, importedDisplay, isStandby, pollRetryable, promotionRunning } from '~/utils/transfer'

/**
 * Moving a server, and a second one kept ready: the import that is running or
 * ran last, exports written to the backup destination, and what a standby
 * holds until it is promoted. Every part is hidden on an agent that has no
 * such endpoint. The export file itself is the CLI's: it is written with a
 * passphrase typed where `shipwick export` runs, and is not downloaded here.
 */
const props = defineProps<{ admin: boolean }>()

const emit = defineEmits<{
  /** A promotion started applications: the server's counts have changed. */
  changed: []
}>()

const agent = useAgent()
const now = useNow()

// --- the import ---------------------------------------------------------------------

// GET /import is 404 NOT_FOUND until an import has run since the agent started: that is "none", not a failure.
const importing = ref(false)
const lastImport = usePolling<Import | null>(async (signal) => {
  try {
    return await agent.get<Import>('/import', { signal })
  }
  catch (cause) {
    if (cause instanceof AgentFailure && cause.code === 'NOT_FOUND') return null
    throw cause
  }
}, { interval: () => (importing.value ? 1500 : 30_000), enabled: () => props.admin })

watch(lastImport.data, (run) => {
  const was = importing.value
  importing.value = run?.status === 'running'
  // What it deployed is on the standby's list, or among the applications, now.
  if (was && !importing.value) {
    void standby.refresh()
    emit('changed')
  }
})

const importRun = computed(() => lastImport.data.value)
const dismissedAt = ref<string | null>(null)
const showOutcome = computed(() => importRun.value !== null && importRun.value.status !== 'running' && importRun.value.started_at !== dismissedAt.value)

// --- exports ------------------------------------------------------------------------

const anyExporting = ref(false)
const exports = usePolling<BackupRun[]>(signal => agent.get<BackupRun[]>('/exports', { query: { limit: 10 }, signal }), {
  interval: () => (anyExporting.value ? 2000 : 60_000),
  enabled: () => props.admin,
})
watch(exports.data, (list) => {
  anyExporting.value = (list ?? []).some(backupBusy)
})
const exportsSupported = computed(() => exports.error.value?.code !== 'ENDPOINT_NOT_FOUND')

const exporting = ref(false)
const exportError = shallowRef<AgentError | null>(null)

async function exportNow() {
  if (exporting.value) return
  exporting.value = true
  exportError.value = null
  try {
    const run = await agent.post<BackupRun>('/exports')
    exports.data.value = [run, ...(exports.data.value ?? []).filter(r => r.id !== run.id)]
    anyExporting.value = true
  }
  catch (cause) {
    exportError.value = toAgentError(cause)
  }
  finally {
    exporting.value = false
  }
}

const took = (r: BackupRun) => (r.completed_at ? formatDuration(durationBetween(r.started_at, r.completed_at)) : '…')

// --- the standby --------------------------------------------------------------------

const standby = usePolling<Standby>(signal => agent.get<Standby>('/standby', { signal }), { interval: 30_000 })
const standbyShown = computed(() => standby.error.value?.code !== 'ENDPOINT_NOT_FOUND' && isStandby(standby.data.value))

const pulling = ref(false)
const pullError = shallowRef<AgentError | null>(null)

async function pullNow() {
  if (pulling.value) return
  pulling.value = true
  pullError.value = null
  try {
    lastImport.data.value = await agent.post<Import>('/standby/pull')
    dismissedAt.value = null
    importing.value = true
    void lastImport.refresh()
  }
  catch (cause) {
    pullError.value = toAgentError(cause)
  }
  finally {
    pulling.value = false
  }
}

// --- the promotion ------------------------------------------------------------------

// A promotion is a record that is started and then followed: it goes on if
// this page is closed, and one that runs when the page opens is shown. An
// agent before 0.6 has no such record (ENDPOINT_NOT_FOUND) and answers the
// request that starts a promotion only when everything has been started.
const followable = ref(true)
const away = ref(false)
/** The record as last answered, to keep on screen while the agent is away. */
const lastRecord = shallowRef<Promotion | null>(null)
const promotionPoll = usePolling<Promotion | null>(async (signal) => {
  try {
    const record = await agent.get<Promotion>('/standby/promotion', { signal })
    away.value = false
    lastRecord.value = record
    return record
  }
  catch (cause) {
    if (cause instanceof AgentFailure) {
      if (cause.code === 'ENDPOINT_NOT_FOUND') {
        followable.value = false
        return null
      }
      // The server was never promoted.
      if (cause.code === 'NOT_FOUND') return null
      // The agent is away for a moment: keep what is on screen and ask again.
      if (promotionRunning(lastRecord.value) && pollRetryable(cause)) {
        away.value = true
        return lastRecord.value
      }
    }
    throw cause
  }
}, { interval: () => (promotionRunning(lastRecord.value) ? 1500 : 30_000), enabled: () => followable.value })

/** What an agent before 0.6 answered at the end of the held request. */
const heldAnswer = shallowRef<Promotion | null>(null)
const promotion = computed(() => (followable.value ? promotionPoll.data.value : heldAnswer.value))
const promotionActive = computed(() => promotionRunning(promotion.value))
const dismissedPromotion = ref<string | null>(null)
const promotionKey = (p: Promotion) => `${p.id ?? 0}/${p.started_at ?? ''}`
const promotionShown = computed(() => promotion.value !== null && (promotion.value.applications.length > 0 || promotionActive.value) && promotionKey(promotion.value) !== dismissedPromotion.value)

watch(promotionActive, (active, was) => {
  // It just ended: what it started is among the applications now, and no longer waits here.
  if (was && !active) {
    void standby.refresh()
    emit('changed')
  }
})

const promoteOpen = ref(false)
const promoting = ref(false)
const promoteError = shallowRef<AgentError | null>(null)
const confirmation = ref('')
const confirmed = computed(() => confirmation.value.trim().toLowerCase() === PROMOTE_WORD)

function openPromote() {
  confirmation.value = ''
  promoteError.value = null
  promoteOpen.value = true
}

async function promote() {
  if (promoting.value || !confirmed.value) return
  promoting.value = true
  promoteError.value = null
  try {
    if (followable.value) {
      // Answered at once with the record as it begins; the panel on the page follows it from there.
      lastRecord.value = await agent.post<Promotion>('/standby/promote', { query: { wait: 'false' } })
      promotionPoll.data.value = lastRecord.value
      dismissedPromotion.value = null
      void promotionPoll.refresh()
    }
    else {
      heldAnswer.value = await agent.post<Promotion>('/standby/promote')
      dismissedPromotion.value = null
      emit('changed')
    }
    promoteOpen.value = false
    void standby.refresh()
  }
  catch (cause) {
    promoteError.value = toAgentError(cause)
    // Somebody else started one: it is followed like our own.
    if (promoteError.value.code === 'PROMOTION_IN_PROGRESS') void promotionPoll.refresh()
  }
  finally {
    promoting.value = false
  }
}

const actionTitle = computed(() => (!props.admin ? roleHint('admin') : promotionActive.value ? 'A promotion is running' : importing.value ? 'An import is running' : undefined))
</script>

<template>
  <!-- The import that is running: said once, above everything it will change. -->
  <div
    v-if="importRun && importRun.status === 'running'"
    class="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-sm border border-warn-line bg-warn-bg px-3 py-2 text-xs text-warn"
    role="status"
  >
    <svg class="spinner" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" aria-hidden="true">
      <path d="M8 2a6 6 0 1 1-6 6" />
    </svg>
    <span class="font-medium">{{ importProgress(importRun) }}</span>
    <span>from {{ importRun.source }}<template v-if="importRun.stopped">, deployed stopped</template>, started <TimeAgo :time="importRun.started_at" /></span>
  </div>

  <!-- The promotion that runs, or ran last: first, because while it runs nothing else here can be started. -->
  <PromotionPanel
    v-if="promotion && promotionShown"
    :promotion="promotion"
    :away="away"
    @dismiss="dismissedPromotion = promotionKey(promotion)"
  />

  <UiPanel v-if="importRun && showOutcome" title="Last import" :meta="importOutcome(importRun)">
    <template #actions>
      <UiButton size="sm" variant="ghost" @click="dismissedAt = importRun.started_at">
        Dismiss
      </UiButton>
    </template>
    <div class="divide-y divide-line">
      <p class="px-4 py-2.5 text-fg-muted">
        From {{ importRun.source }}<template v-if="importRun.exported_at">, written <TimeAgo :time="importRun.exported_at" /></template>;
        finished <TimeAgo :time="importRun.completed_at" /><template v-if="importRun.stopped">, deployed stopped</template><template v-if="importRun.overwrite">, replacing what existed</template>.
        <template v-if="importStored(importRun)">
          Stored {{ importStored(importRun) }}.
        </template>
      </p>
      <p v-if="importRun.error" class="break-words px-4 py-2.5 text-danger" role="alert">
        {{ importRun.error }}
      </p>
      <div v-if="importRun.applications.length > 0" class="overflow-x-auto">
        <table class="data-table stack">
          <thead>
            <tr>
              <th>Application</th>
              <th>Version</th>
              <th>Outcome</th>
              <th>Volumes restored</th>
              <th class="w-[40%]">
                Note
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="a in importRun.applications" :key="a.name">
              <td data-primary>
                <NuxtLink v-if="a.status === 'imported'" :to="`/applications/${a.name}`" class="mono font-medium hover:underline">{{ a.name }}</NuxtLink>
                <span v-else class="mono font-medium">{{ a.name }}</span>
              </td>
              <td data-label="Version" class="mono">
                {{ a.version || '—' }}
              </td>
              <td data-label="Outcome">
                <StatusBadge v-bind="importedDisplay(a)" :raw="a.status" />
              </td>
              <td :data-label="a.volumes.length ? 'Volumes' : undefined" class="mono text-fg-muted" :class="a.volumes.length ? '' : 'max-sm:!hidden'">
                {{ a.volumes.join(', ') }}
              </td>
              <td class="break-words text-fg-muted" :class="[a.status === 'failed' ? '!text-danger' : '', a.message ? 'max-sm:!block max-sm:!text-left' : 'max-sm:!hidden']">
                {{ a.message }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <ul v-if="importRun.warnings.length > 0" class="space-y-1 px-4 py-2.5 text-xs text-warn">
        <li v-for="warning in importRun.warnings" :key="warning" class="flex items-start gap-2">
          <UiIcon name="alert" :size="12" class="mt-[3px]" />
          <span class="min-w-0 break-words">{{ warning }}</span>
        </li>
      </ul>
    </div>
  </UiPanel>

  <!-- What this server holds for the day it has to take over. -->
  <UiPanel v-if="standbyShown && standby.data.value" title="Standby" :meta="standby.data.value.applications.length">
    <template #actions>
      <UiButton v-if="standby.data.value.pull" size="sm" :disabled="!props.admin || importing || promotionActive" :pending="pulling" :title="actionTitle ?? 'Fetches the newest export from the bucket and imports it stopped'" @click="pullNow">
        <UiIcon name="download" :size="12" />
        Import newest now
      </UiButton>
      <UiButton size="sm" variant="primary" :disabled="!props.admin || importing || promotionActive || standby.data.value.applications.length === 0" :title="actionTitle ?? (standby.data.value.applications.length === 0 ? 'Nothing waits to be started' : undefined)" @click="openPromote">
        <UiIcon name="play" :size="12" />
        Promote…
      </UiButton>
    </template>

    <div class="divide-y divide-line">
      <p v-if="standby.data.value.pull" class="px-4 py-2.5 text-fg-muted">
        <span class="label mr-2">Fetches exports</span>{{ describePull(standby.data.value.pull, now) }}
        <span v-if="standby.data.value.pull.last_error" class="mt-1 flex items-start gap-2 text-warn" role="status">
          <UiIcon name="alert" :size="14" class="mt-[3px]" />
          <span class="min-w-0 break-words">The last attempt failed: {{ standby.data.value.pull.last_error }}</span>
        </span>
      </p>
      <InlineError :error="pullError" class="m-3" />
      <EmptyState v-if="standby.data.value.applications.length === 0" title="Nothing waits here yet">
        Applications appear once an export has been imported stopped: by the schedule, with <span class="text-fg">Import newest now</span>, or with <span class="mono text-fg">shipwick import --stopped</span>.
      </EmptyState>
      <div v-else class="overflow-x-auto">
        <table class="data-table stack">
          <thead>
            <tr>
              <th>Application</th>
              <th>Version</th>
              <th>Imported</th>
              <th>Hostnames</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="a in standby.data.value.applications" :key="a.name">
              <td data-primary>
                <NuxtLink :to="`/applications/${a.name}`" class="mono font-medium hover:underline">{{ a.name }}</NuxtLink>
              </td>
              <td data-label="Version" class="mono">
                {{ a.version || '—' }}
              </td>
              <td data-label="Imported" class="text-fg-muted">
                <TimeAgo :time="a.imported_at" />
              </td>
              <td data-label="Hostnames" class="mono text-fg-muted">
                <span v-if="a.hostnames.length > 0" class="block">
                  <span v-for="host in a.hostnames" :key="host" class="block break-words">{{ host }}</span>
                </span>
                <span v-else class="text-fg-faint">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="px-4 py-2 text-xs text-fg-subtle">
        These applications are deployed and stopped, with their volumes filled from the export. A promotion starts them in the order they were imported and names the DNS records to point here; an export that arrives afterwards no longer replaces them.
      </p>
    </div>
  </UiPanel>

  <!-- Admin only: the list and the request are refused to every other role. -->
  <UiPanel v-if="props.admin && exportsSupported" title="Export">
    <template #actions>
      <UiButton size="sm" :disabled="anyExporting" :pending="exporting" :title="anyExporting ? 'An export is being written' : 'Writes an export of every application to where backups are kept'" @click="exportNow">
        <UiIcon name="upload" :size="12" />
        Export to backups
      </UiButton>
    </template>
    <div class="divide-y divide-line">
      <InlineError :error="exportError" class="m-3" />
      <TableSkeleton v-if="exports.loading.value && !exports.data.value" :rows="2" :columns="6" />
      <ErrorState
        v-else-if="exports.error.value && !exports.data.value"
        :error="exports.error.value"
        subject="the exports"
        :retrying="exports.refreshing.value"
        @retry="exports.refresh()"
      />
      <EmptyState v-else-if="(exports.data.value?.length ?? 0) === 0" title="No exports in the backups">
        An export is everything another server needs to run what this one runs: every application's configuration, the stored secrets, registry credentials and certificates, images built by the CLI, static folders and the volumes. Write one now, or on a schedule with <span class="mono text-fg">SHIPWICK_EXPORT_SCHEDULE</span> on the agent.
      </EmptyState>
      <div v-else class="overflow-x-auto">
        <table class="data-table stack">
          <thead>
            <tr>
              <th class="w-16">
                Export
              </th>
              <th>Written</th>
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
            <tr v-for="r in exports.data.value" :key="r.id">
              <td data-primary class="mono">
                #{{ r.id }}
              </td>
              <td data-label="Written" class="text-fg-muted">
                <TimeAgo :time="r.started_at" />
              </td>
              <td data-label="Started by" class="text-fg-muted">
                {{ triggerLabel(r.trigger) }}
              </td>
              <td data-label="Size" class="mono right whitespace-nowrap text-fg-muted">
                {{ backupUsable(r) ? formatBytes(backupSize(r)) : '—' }}
              </td>
              <td data-label="Kept in" class="mono whitespace-nowrap text-fg-muted">
                {{ describeDestinations(r) }}
              </td>
              <td data-label="Status">
                <StatusBadge v-bind="backupStatusDisplay(r)" :raw="r.status" />
                <span v-if="r.status === 'failed' && r.error" class="block max-w-sm truncate text-xs text-danger" :title="r.error">{{ r.error }}</span>
              </td>
              <td data-label="Took" class="mono right text-fg-muted">
                {{ took(r) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="px-4 py-2 text-xs text-fg-subtle">
        These are written encrypted with the agent's backup passphrase, for a standby to fetch or for <span class="mono">shipwick import</span> on another server. An export as a file of your own is the CLI's: <span class="mono">shipwick export</span> asks for a passphrase where it runs and streams the file there; the dashboard does not download one.
      </p>
    </div>
  </UiPanel>

  <UiDialog :open="promoteOpen" title="Promote this standby" size="md" :busy="promoting" @close="promoteOpen = false">
    <form v-if="standby.data.value" id="promote-form" class="space-y-4" @submit.prevent="promote">
      <p class="text-fg-muted">
        This starts the {{ standby.data.value.applications.length === 1 ? 'application' : `${standby.data.value.applications.length} applications` }} this server holds stopped
        (<span class="mono text-fg">{{ standby.data.value.applications.map(a => a.name).join(', ') }}</span>), in the order they were imported, each once the one before it is ready.
        From then on this server is the one that serves: exports that arrive later no longer replace what runs here. Do it when the first server is gone or about to be.
      </p>
      <p v-if="followable" class="text-fg-muted">
        The promotion runs on the server and is shown on this page as it goes; it continues if you close the page, and across a restart of the agent.
      </p>
      <div>
        <label for="promote-confirm" class="block text-fg-muted">
          Type <span class="mono select-all font-medium text-fg">{{ PROMOTE_WORD }}</span> to confirm
        </label>
        <input
          id="promote-confirm"
          v-model="confirmation"
          type="text"
          class="input mono mt-1.5"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          autofocus
        >
      </div>
      <p v-if="promoting && !followable" class="text-xs text-fg-muted" role="status">
        Starting the applications. This agent answers when the last one is ready, which can take as long as their startup budgets together.
      </p>
      <InlineError :error="promoteError" />
    </form>
    <template #footer>
      <UiButton :disabled="promoting" @click="promoteOpen = false">
        Cancel
      </UiButton>
      <UiButton type="submit" form="promote-form" variant="primary" :pending="promoting" :disabled="!confirmed">
        Promote
      </UiButton>
    </template>
  </UiDialog>
</template>
