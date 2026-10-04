<script setup lang="ts">
import type { AppSpec, BackupRun } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { backupAnnouncement } from '~/utils/announce'
import { toAgentError } from '~/utils/agentError'
import { formatBytes } from '~/utils/format'
import { backupBusy, backupSize, backupStatusDisplay, backupUsable, describeBackups, describeDestinations, triggerLabel, verificationDisplay } from '~/utils/backups'
import { roleHint } from '~/utils/roles'

/**
 * The backups the agent took of the application's volumes: on the schedule
 * under `backups` in deploy.yaml, or on request. Taking and verifying one
 * needs the deploy role; restoring, downloading and removing need admin, and
 * live in the dialog a row opens.
 */
const props = defineProps<{
  application: string
  spec: AppSpec
  mayDeploy: boolean
  admin: boolean
  /** A restore needs the application stopped. */
  stopped: boolean
  /** A deployment or another operation holds the application. */
  busy: boolean
}>()

const emit = defineEmits<{
  /** A backup was taken, verified, restored or removed: the application's events have a new line. */
  changed: []
}>()

const agent = useAgent()
const now = useNow()
const { announce } = useAnnounce()
const path = computed(() => `/applications/${encodeURIComponent(props.application)}/backups`)

// A backup being taken, verified or restored is followed closely; the list is otherwise quiet.
const anyBusy = ref(false)
const runs = usePolling<BackupRun[]>(signal => agent.get<BackupRun[]>(path.value, { query: { limit: 25 }, signal }), {
  interval: () => (anyBusy.value ? 2000 : 30_000),
})
watch(runs.data, (list, before) => {
  const was = anyBusy.value
  anyBusy.value = (list ?? []).some(backupBusy)
  // Something just finished: its event is in the application's feed now.
  if (was && !anyBusy.value && before) emit('changed')
  // The row changes without a sound: how it ended is said.
  if (list && before) announce(backupAnnouncement(before, list))
})
watch(() => props.application, () => void runs.reset())

/** An agent from before it took backups by itself: the panel has nothing to offer. */
const unsupported = computed(() => runs.error.value?.code === 'ENDPOINT_NOT_FOUND')

const summary = computed(() => describeBackups(props.spec.backups, runs.data.value ?? [], now.value))
const SUMMARY_TEXT = { ok: 'text-fg-muted', warn: 'text-warn', danger: 'text-danger', muted: 'text-fg-muted' } as const

// --- taking and verifying -------------------------------------------------------

const actionError = shallowRef<AgentError | null>(null)
const starting = ref(false)
const verifying = ref<number | null>(null)
const openId = ref<number | null>(null)

/** Puts an answered backup into the list at once, so its row does not wait for the next poll. */
function merge(run: BackupRun) {
  const list = runs.data.value ?? []
  runs.data.value = list.some(r => r.id === run.id) ? list.map(r => (r.id === run.id ? run : r)) : [run, ...list]
}

async function backUpNow() {
  if (starting.value) return
  starting.value = true
  actionError.value = null
  try {
    merge(await agent.post<BackupRun>(path.value))
  }
  catch (cause) {
    actionError.value = toAgentError(cause)
  }
  finally {
    starting.value = false
  }
}

async function verify(run: BackupRun) {
  if (verifying.value !== null) return
  verifying.value = run.id
  actionError.value = null
  try {
    merge(await agent.post<BackupRun>(`${path.value}/${run.id}/verify`))
  }
  catch (cause) {
    actionError.value = toAgentError(cause)
    void runs.refresh()
  }
  finally {
    verifying.value = null
  }
}

function onDialogChanged() {
  void runs.refresh()
  emit('changed')
}

const { deployHint } = useApplication()
const blocked = computed(() => deployHint.value ?? roleHint('deploy'))
const adopting = ref(false)

const startTitle = computed(() => (!props.mayDeploy ? blocked.value : props.busy ? 'Another operation is in progress' : anyBusy.value ? 'A backup is in progress' : undefined))
const verifyTitle = (run: BackupRun) => (!props.mayDeploy ? blocked.value : backupBusy(run) ? 'This backup is in use' : 'Restores the backup into scratch volumes and starts one container on them, beside the application')

function openRow(run: BackupRun, event: MouseEvent) {
  if ((event.target as HTMLElement).closest('a, button') || window.getSelection()?.toString()) return
  openId.value = run.id
}
</script>

<template>
  <UiPanel v-if="!unsupported" title="Backups" :meta="runs.data.value?.length ?? null">
    <template #actions>
      <UiButton v-if="props.admin" size="sm" variant="ghost" hint="Record backups of this application that are in the backup destination and missing from this list" @click="adopting = true">
        Adopt…
      </UiButton>
      <UiButton size="sm" :disabled="!props.mayDeploy || props.busy || anyBusy" :pending="starting" :hint="startTitle" @click="backUpNow">
        <UiIcon name="download" :size="12" />
        Back up now
      </UiButton>
    </template>

    <div class="divide-y divide-line">
      <p class="px-4 py-2.5" :class="SUMMARY_TEXT[summary.tone]">
        <span class="label mr-2">Schedule</span>{{ summary.text }}
      </p>
      <InlineError :error="actionError" class="m-3" />

      <TableSkeleton v-if="runs.loading.value && !runs.data.value" :rows="3" :columns="6" />
      <ErrorState
        v-else-if="runs.error.value && !runs.data.value"
        :error="runs.error.value"
        subject="the backups"
        :retrying="runs.refreshing.value"
        @retry="runs.refresh()"
      />
      <EmptyState v-else-if="(runs.data.value?.length ?? 0) === 0" title="No backups yet">
        <template v-if="props.spec.backups">
          The first one is taken when the schedule next comes round, or now with <span class="text-fg">Back up now</span>.
        </template>
        <template v-else>
          Add a <span class="mono text-fg">backups</span> block to deploy.yaml to have the volumes archived on a schedule, or take one now with <span class="text-fg">Back up now</span>.
        </template>
      </EmptyState>
      <div v-else class="overflow-x-auto">
        <table class="data-table stack" aria-label="Backups">
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
              <th>Verified</th>
              <th class="right">
                <span class="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in runs.data.value" :key="r.id" class="clickable" @click="openRow(r, $event)">
              <td data-primary>
                <button type="button" class="mono link" :aria-label="`Backup ${r.id}`" @click="openId = r.id">#{{ r.id }}</button>
              </td>
              <td data-label="Taken" class="text-fg-muted">
                <TimeAgo :time="r.started_at" />
              </td>
              <td data-label="Started by" class="text-fg-muted">
                <UiTooltip :text="r.trigger === 'adopted' ? 'Found in the backup destination and recorded afterwards: how it was taken is not known, and what it holds was read from its files alone' : null">{{ triggerLabel(r.trigger) }}</UiTooltip>
              </td>
              <td data-label="Size" class="mono right whitespace-nowrap text-fg-muted">
                {{ backupUsable(r) ? formatBytes(backupSize(r)) : '—' }}
              </td>
              <td data-label="Kept in" class="mono whitespace-nowrap text-fg-muted">
                <span class="inline-flex items-center gap-1.5">
                  {{ describeDestinations(r) }}
                  <UiTooltip v-if="r.encrypted && backupUsable(r)" repeats text="Encrypted with the agent's passphrase" class="inline-flex text-fg-subtle">
                    <UiIcon name="lock" :size="12" /><span class="sr-only">encrypted</span>
                  </UiTooltip>
                </span>
              </td>
              <td data-label="Status">
                <StatusBadge v-bind="backupStatusDisplay(r)" :raw="r.activity || r.status" />
                <UiTooltip v-if="r.status === 'failed' && r.error" repeats :text="r.error" class="block max-w-[15rem] truncate text-xs text-danger">{{ r.error }}</UiTooltip>
              </td>
              <td data-label="Verified">
                <StatusBadge v-if="verificationDisplay(r, now)" v-bind="verificationDisplay(r, now)!" :detail="r.verify_error || undefined" :raw="r.verified_at || undefined" />
                <span v-else-if="r.trigger === 'adopted' && backupUsable(r)" class="text-xs text-warn">not yet: verify before relying on it</span>
                <span v-else class="text-fg-subtle">never</span>
              </td>
              <td class="right">
                <UiButton
                  v-if="backupUsable(r)"
                  size="sm"
                  :disabled="!props.mayDeploy || backupBusy(r)"
                  :pending="verifying === r.id"
                  :hint="verifyTitle(r)"
                  :aria-label="`Verify backup ${r.id}`"
                  @click="verify(r)"
                >
                  Verify
                </UiButton>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="px-4 py-2 text-xs text-fg-subtle">
        A backup that failed keeps nothing. Verifying proves a backup restores: a container of the current image is started on a copy of it and held to the health check, and the application is not touched.
        <template v-if="props.admin">
          Open a backup to download, restore or remove it.
        </template>
        <template v-else>
          Restoring, downloading and removing need the admin role.
        </template>
      </p>
    </div>

    <AdoptDialog :open="adopting" :application="props.application" @close="adopting = false" @adopted="runs.refresh()" />

    <BackupDialog
      :open="openId !== null"
      :application="props.application"
      :backup-id="openId"
      :may-deploy="props.mayDeploy"
      :admin="props.admin"
      :stopped="props.stopped"
      :busy="props.busy"
      @close="openId = null"
      @changed="onDialogChanged"
    />
  </UiPanel>
</template>
