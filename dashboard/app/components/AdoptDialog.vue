<script setup lang="ts">
import type { AdoptedBackup, BackupAdoption, BackupAdoptRequest } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { backupSize, describeDestinations } from '~/utils/backups'
import { formatBytes, pluralize } from '~/utils/format'

/**
 * Adopting backups: the agent looks through its backup directory and bucket
 * and records what its database does not know — what a database restored
 * from an older backup of the agent's state has forgotten. With
 * `application`, only that application's.
 */
const props = defineProps<{ open: boolean, application?: string }>()
const emit = defineEmits<{
  close: []
  /** Backups were recorded: the lists that show them are out of date. */
  adopted: []
}>()

const agent = useAgent()
const pending = ref(false)
const error = shallowRef<AgentError | null>(null)
const result = shallowRef<BackupAdoption | null>(null)

watch(() => props.open, (open) => {
  if (!open) return
  error.value = null
  result.value = null
})

async function adopt() {
  if (pending.value) return
  pending.value = true
  error.value = null
  try {
    const body: BackupAdoptRequest = props.application ? { application: props.application } : {}
    result.value = await agent.post<BackupAdoption>('/server/backups/adopt', { body })
    if (result.value.adopted.length > 0) emit('adopted')
  }
  catch (cause) {
    error.value = toAgentError(cause)
  }
  finally {
    pending.value = false
  }
}

/** "postgres", "the agent's state", "an export": what a found backup is of. */
function subject(row: { kind: string, application: string }): string {
  if (row.kind === 'state') return 'the agent\'s state'
  if (row.kind === 'export') return 'an export'
  return row.application
}

const where = (row: AdoptedBackup) => (row.kind === 'application' ? `/applications/${encodeURIComponent(row.application)}/backups` : null)
const nothing = computed(() => result.value !== null && result.value.adopted.length === 0 && result.value.skipped.length === 0)
</script>

<template>
  <UiDialog :open="props.open" :title="props.application ? `Adopt backups of ${props.application}` : 'Adopt backups'" size="md" :busy="pending" @close="emit('close')">
    <div v-if="result" class="space-y-4">
      <p v-if="nothing" class="flex items-start gap-2 text-fg-muted">
        <UiIcon name="check" :size="14" class="mt-[3px] text-ok" />
        Nothing to adopt: every backup the destination holds is in the list already.
      </p>
      <div v-if="result.adopted.length > 0">
        <p class="label mb-1.5">
          Adopted · {{ result.adopted.length }}
        </p>
        <div class="overflow-x-auto rounded-sm border border-line">
          <table class="data-table stack !text-xs">
            <thead>
              <tr>
                <th>Backup of</th>
                <th>Number</th>
                <th>Taken</th>
                <th class="right">
                  Size
                </th>
                <th>Kept in</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in result.adopted" :key="row.backup.id">
                <td data-primary>
                  <NuxtLink v-if="where(row)" :to="where(row)!" class="mono font-medium hover:underline">{{ subject(row) }}</NuxtLink>
                  <span v-else class="font-medium">{{ subject(row) }}</span>
                </td>
                <td data-label="Number" class="mono">
                  #{{ row.backup.id }}
                </td>
                <td data-label="Taken" class="text-fg-muted">
                  <TimeAgo :time="row.backup.started_at" />
                </td>
                <td data-label="Size" class="mono right whitespace-nowrap text-fg-muted">
                  {{ formatBytes(backupSize(row.backup)) }}
                </td>
                <td data-label="Kept in" class="mono whitespace-nowrap text-fg-muted">
                  {{ describeDestinations(row.backup) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="mt-1.5 text-xs text-fg-muted">
          {{ result.adopted.length === 1 ? 'It is' : 'They are' }} listed with the other backups from now on. What an application's backup holds is read from its files alone, so verify one before you rely on it.
        </p>
      </div>
      <div v-if="result.skipped.length > 0">
        <p class="label mb-1.5">
          Left alone · {{ result.skipped.length }}
        </p>
        <ul class="divide-y divide-line rounded-sm border border-line text-xs">
          <li v-for="row in result.skipped" :key="`${row.kind}/${row.application}/${row.id}`" class="px-3 py-2">
            <span class="font-medium">Backup <span class="mono">#{{ row.id }}</span> of {{ subject(row) }}</span>
            <span class="mt-0.5 block break-words text-fg-muted">{{ row.reason }}</span>
          </li>
        </ul>
      </div>
    </div>
    <div v-else class="space-y-4">
      <p class="text-fg-muted">
        The agent looks through its backup directory and the bucket for backups{{ props.application ? ` of ${props.application}` : '' }} that its list does not have, and records them. That is what is needed after the agent's own state was restored from a backup: the backups taken since are still there, and the restored database does not know them.
      </p>
      <p class="text-fg-muted">
        Nothing is written, moved or removed in the destination, and a backup is never recorded twice.
      </p>
      <InlineError :error="error" />
    </div>
    <template #footer>
      <template v-if="result">
        <span v-if="!nothing" class="mr-auto text-xs text-fg-muted">{{ pluralize(result.adopted.length, 'backup') }} adopted</span>
        <UiButton autofocus @click="emit('close')">
          Close
        </UiButton>
      </template>
      <template v-else>
        <UiButton :disabled="pending" @click="emit('close')">
          Cancel
        </UiButton>
        <UiButton variant="primary" :pending="pending" autofocus @click="adopt">
          Look for backups
        </UiButton>
      </template>
    </template>
  </UiDialog>
</template>
