<script setup lang="ts">
import type { BackupRunDetail, BackupVolume } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { agentUrl } from '~/composables/useAgent'
import { durationBetween, formatAbsoluteUtc, formatBytes, formatDuration } from '~/utils/format'
import { backupBusy, backupStatusDisplay, backupUsable, describeDestinations } from '~/utils/backups'
import { roleHint } from '~/utils/roles'

/**
 * One backup: what it holds, whether it has proven to restore, and the three
 * things an admin does with it. A verification or a restore is polled until
 * `activity` is empty again, the same rule as `completed_at` for the backup
 * itself: the status alone does not end it.
 */
const props = defineProps<{
  open: boolean
  application: string
  backupId: number | null
  mayDeploy: boolean
  admin: boolean
  /** A restore needs the application stopped; the button says so otherwise. */
  stopped: boolean
  busy: boolean
}>()

const emit = defineEmits<{ close: [], changed: [] }>()

const agent = useAgent()
const { deployHint } = useApplication()
const path = computed(() => `/applications/${encodeURIComponent(props.application)}/backups/${props.backupId}`)

const backup = usePolling<BackupRunDetail>(
  signal => agent.get<BackupRunDetail>(path.value, { signal }),
  { interval: 1500, enabled: () => props.open && props.backupId !== null, until: b => !backupBusy(b) },
)

const b = computed(() => backup.data.value)
const status = computed(() => (b.value ? backupStatusDisplay(b.value) : null))
const inUse = computed(() => (b.value ? backupBusy(b.value) : false))
const usable = computed(() => (b.value ? backupUsable(b.value) : false))

const duration = computed(() => (b.value?.completed_at ? formatDuration(durationBetween(b.value.started_at, b.value.completed_at)) : '—'))

// --- actions ----------------------------------------------------------------------

type Mode = 'view' | 'restore' | 'remove'
const mode = ref<Mode>('view')
const pending = ref(false)
const error = shallowRef<AgentError | null>(null)
const confirmation = ref('')

watch(() => [props.open, props.backupId], () => {
  mode.value = 'view'
  error.value = null
  confirmation.value = ''
  if (props.open) void backup.reset()
})

/** Runs one request against the backup; a 202 answers the backup with its new activity, which is then followed. */
async function act(run: () => Promise<BackupRunDetail | void>, after?: () => void) {
  if (pending.value) return
  pending.value = true
  error.value = null
  try {
    const answer = await run()
    mode.value = 'view'
    confirmation.value = ''
    if (answer) {
      // The answer to a 202 carries no verify_output; keep the one on screen until the next poll.
      backup.data.value = { ...answer, verify_output: answer.verify_output ?? b.value?.verify_output ?? '' }
      backup.resume()
    }
    emit('changed')
    after?.()
  }
  catch (cause) {
    error.value = toAgentError(cause)
    void backup.refresh()
  }
  finally {
    pending.value = false
  }
}

const verify = () => act(() => agent.post<BackupRunDetail>(`${path.value}/verify`))
const restore = () => act(() => agent.post<BackupRunDetail>(`${path.value}/restore`))
const remove = () => act(() => agent.del(path.value), () => emit('close'))

// The outcome of a verification or a restore is an event in the application's feed.
watch(inUse, (now, before) => {
  if (before && !now) emit('changed')
})

const confirmed = computed(() => confirmation.value === props.application)

function archiveUrl(volume: BackupVolume): string {
  // `follow` takes the request around the proxy's encoder, which would hold the
  // response's header back until the agent has fetched and decrypted the first chunk.
  return agentUrl(`${path.value}/volumes/${encodeURIComponent(volume.volume)}/archive`, { follow: 'true' })
}

const restoreTitle = computed(() => {
  if (!props.admin) return roleHint('admin')
  if (inUse.value) return 'This backup is in use'
  if (props.busy) return 'Another operation is in progress'
  if (!props.stopped) return 'Stop the application first'
  return undefined
})

const title = computed(() => `Backup #${props.backupId ?? ''} of ${props.application}`)
</script>

<template>
  <UiDialog :open="props.open" :title="title" size="md" :busy="pending" @close="emit('close')">
    <div v-if="backup.loading.value && !b" class="space-y-3" role="progressbar" aria-busy="true" aria-label="Loading">
      <span class="skeleton w-40" />
      <span class="skeleton h-24 w-full" />
    </div>
    <ErrorState v-else-if="!b && backup.error.value" :error="backup.error.value" subject="the backup" :retrying="backup.refreshing.value" class="!px-0 !py-2" @retry="backup.refresh()" />

    <!-- Restore: replaces data, so the name is typed, as for deleting the application. -->
    <form v-else-if="b && mode === 'restore'" id="backup-restore-form" class="space-y-4" @submit.prevent="confirmed && restore()">
      <p class="text-fg-muted">
        This replaces everything in
        <span class="mono text-fg">{{ b.volumes.map(v => v.volume).join(', ') }}</span>
        of <span class="mono text-fg">{{ props.application }}</span> with what backup #{{ b.id }} holds, taken <TimeAgo :time="b.started_at" />. What the volumes hold now is gone; it cannot be undone. The application stays stopped afterwards.
      </p>
      <div>
        <label for="backup-restore-confirm" class="block text-fg-muted">
          Type <span class="mono select-all font-medium text-fg">{{ props.application }}</span> to confirm
        </label>
        <input
          id="backup-restore-confirm"
          v-model="confirmation"
          type="text"
          class="input mono mt-1.5"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          autofocus
        >
      </div>
      <InlineError :error="error" />
    </form>

    <div v-else-if="b && mode === 'remove'" class="space-y-4">
      <p class="text-fg-muted">
        Backup #{{ b.id }} is removed from {{ b.destinations.length > 1 ? 'every place it is kept in' : 'where it is kept' }}<template v-if="b.destinations.length > 0"> (<span class="mono text-fg">{{ describeDestinations(b) }}</span>)</template>, then its record. It cannot be restored afterwards.
      </p>
      <InlineError :error="error" />
    </div>

    <div v-else-if="b && status" class="space-y-4">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
        <StatusBadge :tone="status.tone" :label="status.label" :raw="b.activity || b.status" size="md" />
        <span class="text-fg-muted">{{ b.trigger === 'schedule' ? 'taken by the schedule' : b.trigger === 'adopted' ? 'found in the backup destination and adopted' : 'taken by hand' }}</span>
      </div>

      <dl class="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-sm">
        <dt class="label pt-0.5">
          Taken
        </dt>
        <dd>
          <TimeAgo :time="b.started_at" /><span class="mono ml-2 text-xs text-fg-subtle">{{ formatAbsoluteUtc(b.started_at) }}</span>
          <span v-if="b.completed_at" class="text-fg-muted"> · took <span class="mono">{{ duration }}</span></span>
        </dd>
        <template v-if="usable">
          <dt class="label pt-0.5">
            Kept in
          </dt>
          <dd>
            <span class="mono">{{ describeDestinations(b) }}</span>
            <span class="text-fg-muted"> · {{ b.encrypted ? 'encrypted with the agent\'s passphrase' : 'not encrypted' }}</span>
          </dd>
          <dt class="label pt-0.5">
            Volumes
          </dt>
          <dd class="space-y-1">
            <div v-for="v in b.volumes" :key="v.volume" class="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span class="mono">{{ v.volume }}</span>
              <span class="mono text-fg-muted">{{ formatBytes(v.size_bytes) }}</span>
              <a
                v-if="props.admin && !inUse"
                :href="archiveUrl(v)"
                download
                class="inline-flex h-6 items-center gap-1.5 whitespace-nowrap rounded-sm border border-line-strong bg-bg px-2 text-xs font-medium hover:bg-hover"
                :title="`Downloads ${v.volume} as it was in this backup: a tar archive, decrypted`"
              >
                <UiIcon name="download" :size="12" />
                Download
              </a>
            </div>
          </dd>
        </template>
        <template v-if="b.error">
          <dt class="label pt-0.5">
            Error
          </dt>
          <dd class="break-words text-danger">
            {{ b.error }} <span class="text-fg-muted">Nothing was kept of this backup.</span>
          </dd>
        </template>
        <template v-if="usable">
          <dt class="label pt-0.5">
            Verified
          </dt>
          <dd>
            <template v-if="b.activity === 'verify'">
              <span class="text-warn">A container is being started on a copy of it…</span>
            </template>
            <template v-else-if="b.verify_error">
              <span class="break-words text-danger">{{ b.verify_error }}</span>
            </template>
            <template v-else-if="b.verified_at">
              <span class="text-ok">It restored, and the application came up on it</span> <TimeAgo :time="b.verified_at" class="text-fg-muted" />
            </template>
            <span v-else class="text-fg-muted">Never: nothing has shown yet that this backup restores.</span>
          </dd>
          <template v-if="b.activity === 'restore' || b.restore_error || b.restored_at">
            <dt class="label pt-0.5">
              Restored
            </dt>
            <dd>
              <span v-if="b.activity === 'restore'" class="text-warn">The volumes are being replaced…</span>
              <span v-else-if="b.restore_error" class="break-words text-danger">{{ b.restore_error }}</span>
              <template v-else>
                <span class="text-ok">Into the application's volumes</span> <TimeAgo :time="b.restored_at" class="text-fg-muted" /><span class="text-fg-muted">. The application stays stopped until it is started.</span>
              </template>
            </dd>
          </template>
        </template>
      </dl>

      <div v-if="b.verify_output">
        <p class="label mb-1.5">
          Output of the verification <span class="normal-case tracking-normal text-fg-subtle">last 200 lines</span>
        </p>
        <pre class="mono max-h-64 overflow-auto whitespace-pre rounded-sm border border-line bg-inset px-3 py-2 text-xs leading-[1.125rem]" tabindex="0">{{ b.verify_output }}</pre>
      </div>
      <InlineError :error="error" />
    </div>

    <template #footer>
      <template v-if="b && mode === 'restore'">
        <UiButton :disabled="pending" @click="mode = 'view'">
          Cancel
        </UiButton>
        <UiButton type="submit" form="backup-restore-form" variant="danger-solid" :pending="pending" :disabled="!confirmed">
          Restore backup
        </UiButton>
      </template>
      <template v-else-if="b && mode === 'remove'">
        <UiButton :disabled="pending" autofocus @click="mode = 'view'">
          Cancel
        </UiButton>
        <UiButton variant="danger-solid" :pending="pending" @click="remove">
          Remove backup
        </UiButton>
      </template>
      <template v-else>
        <template v-if="b">
          <UiButton v-if="props.admin" variant="danger" class="mr-auto" :disabled="inUse || pending" :title="inUse ? 'This backup is in use' : undefined" @click="mode = 'remove'">
            <UiIcon name="trash" :size="12" />
            Remove
          </UiButton>
          <template v-if="usable">
            <UiButton :disabled="!props.mayDeploy || inUse" :pending="pending" :title="!props.mayDeploy ? (deployHint ?? roleHint('deploy')) : inUse ? 'This backup is in use' : undefined" @click="verify">
              Verify
            </UiButton>
            <UiButton :disabled="!props.admin || inUse || props.busy || !props.stopped" :title="restoreTitle" @click="mode = 'restore'">
              <UiIcon name="upload" :size="12" />
              Restore…
            </UiButton>
          </template>
        </template>
        <UiButton @click="emit('close')">
          Close
        </UiButton>
      </template>
    </template>
  </UiDialog>
</template>
