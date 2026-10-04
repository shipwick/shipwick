<script setup lang="ts">
import type { VolumeInfo } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { roleHint } from '~/utils/roles'
import { formatVolumeSize, removable, sortVolumes, volumeRemovalConsequence, volumeStatusDisplay } from '~/utils/volumes'

useHead({ title: 'Volumes' })

const agent = useAgent()
const access = useAccess()

/** Every volume Shipwick created on the server, with the application it belongs to and whether that application still exists. */
const volumes = usePolling<VolumeInfo[]>(signal => agent.get<VolumeInfo[]>('/volumes', { signal }), { interval: 30_000 })

const rows = computed(() => sortVolumes(volumes.data.value ?? []))
const orphans = computed(() => rows.value.filter(removable).length)

const mayAdmin = computed(() => access.can('admin'))
const removeHint = computed(() => (mayAdmin.value ? undefined : roleHint('admin')))

// --- remove ---------------------------------------------------------------------

const removing = ref<VolumeInfo | null>(null)
const removePending = ref(false)
const removeError = shallowRef<AgentError | null>(null)

async function remove() {
  const target = removing.value
  if (!target || removePending.value) return
  removePending.value = true
  removeError.value = null
  try {
    await agent.del(`/volumes/${encodeURIComponent(target.name)}`)
    removing.value = null
    void volumes.refresh()
  }
  catch (cause) {
    removeError.value = toAgentError(cause)
    // Gone already, or its application is back: the list on screen was stale either way.
    if (removeError.value.notFound || removeError.value.code === 'VOLUME_IN_USE') void volumes.refresh()
  }
  finally {
    removePending.value = false
  }
}

function closeRemove() {
  removing.value = null
  removeError.value = null
}
</script>

<template>
  <div>
    <PageHeader :crumbs="[{ label: 'Volumes' }]" />
    <PageBody>
      <StaleNotice :error="volumes.data.value ? volumes.error.value : null" :updated-at="volumes.updatedAt.value" />

      <UiPanel title="Volumes" :meta="volumes.data.value ? `${volumes.data.value.length}${orphans ? `, ${orphans} of deleted applications` : ''}` : null">
        <TableSkeleton v-if="volumes.loading.value" :rows="3" :columns="4" />
        <ErrorState
          v-else-if="volumes.error.value && !volumes.data.value"
          :error="volumes.error.value"
          subject="volumes"
          :retrying="volumes.refreshing.value"
          @retry="volumes.refresh()"
        />
        <EmptyState v-else-if="rows.length === 0" title="No volumes">
          A volume appears here once an application with <span class="mono text-fg">volumes</span> in its deploy.yaml has been deployed.
        </EmptyState>
        <div v-else class="overflow-x-auto">
          <table class="data-table stack" aria-label="Volumes">
            <thead>
              <tr>
                <th>Name</th>
                <th>Application</th>
                <th class="right">
                  Size
                </th>
                <th>Status</th>
                <th class="right">
                  <span class="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="v in rows" :key="v.name">
                <td data-primary class="mono font-medium">
                  {{ v.name }}
                </td>
                <td data-label="Application" class="mono">
                  <NuxtLink v-if="!v.orphan" :to="`/applications/${v.application}`" class="hover:underline">{{ v.application }}</NuxtLink>
                  <UiTooltip v-else class="text-fg-muted" text="This application has been deleted; its volume was kept on purpose">{{ v.application }}</UiTooltip>
                  <span class="ml-1.5 text-xs text-fg-subtle">{{ v.volume }}</span>
                </td>
                <td data-label="Size" class="mono right" :class="v.size_bytes < 0 ? 'text-fg-subtle' : ''">
                  <UiTooltip :text="v.size_bytes < 0 ? 'The Docker daemon reports no size for this volume' : null">{{ formatVolumeSize(v.size_bytes) }}</UiTooltip>
                </td>
                <td data-label="Status">
                  <StatusBadge v-bind="volumeStatusDisplay(v)" />
                </td>
                <td class="right">
                  <UiButton v-if="removable(v)" variant="danger" size="sm" :disabled="!mayAdmin" :hint="removeHint" :aria-label="`Remove ${v.name}`" @click="removing = v">
                    Remove
                  </UiButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="border-t border-line px-4 py-2 text-xs text-fg-subtle">
          Deleting an application keeps its volumes, so its data survives a mistake; they are listed here as <em>application deleted</em> until removed.
          The volume of an existing application is replaced through a restore on the application's page, never removed here.
        </p>
      </UiPanel>
    </PageBody>

    <ConfirmDialog
      :open="removing !== null"
      :title="`Remove ${removing?.name ?? ''}`"
      confirm-label="Remove volume"
      danger
      :pending="removePending"
      :error="removeError"
      @close="closeRemove"
      @confirm="remove"
    >
      <template v-if="removing">
        {{ volumeRemovalConsequence(removing) }}
      </template>
    </ConfirmDialog>
  </div>
</template>
