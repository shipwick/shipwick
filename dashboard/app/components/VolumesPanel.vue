<script setup lang="ts">
import type { SpecVolume } from '~/types/api'
import { agentUrl } from '~/composables/useAgent'

/**
 * The application's volumes, with a backup download and a restore per volume.
 * Both need the admin role: a backup carries the application's data, a
 * restore replaces it. The download is a plain link through the proxy, so the
 * browser streams gigabytes to disk and takes the filename from the agent's
 * Content-Disposition.
 */
const props = defineProps<{
  application: string
  volumes: SpecVolume[]
  /** The signed-in token may take backups and restore. */
  admin: boolean
  /** A restore needs the application stopped; the button says so otherwise. */
  stopped: boolean
  /** A deployment or another operation holds the application. */
  busy: boolean
}>()

const emit = defineEmits<{ restore: [volume: SpecVolume] }>()

function archiveUrl(volume: SpecVolume): string {
  return agentUrl(`/applications/${encodeURIComponent(props.application)}/volumes/${encodeURIComponent(volume.name)}/archive`)
}

const restoreTitle = computed(() => {
  if (props.busy) return 'Another operation is in progress'
  if (!props.stopped) return 'Stop the application first'
  return undefined
})
</script>

<template>
  <div class="overflow-x-auto">
    <table class="data-table stack">
      <thead>
        <tr>
          <th>Volume</th>
          <th>Mounted at</th>
          <th v-if="props.admin" class="right">
            Backup
          </th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="v in props.volumes" :key="v.name">
          <td data-primary class="mono font-medium">
            {{ v.name }}
          </td>
          <td data-label="Mounted at" class="mono text-fg-muted">
            {{ v.path }}
          </td>
          <td v-if="props.admin" class="right">
            <span class="inline-flex flex-wrap items-center justify-end gap-2">
              <a
                :href="archiveUrl(v)"
                download
                class="inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded-sm border border-line-strong bg-bg px-2.5 text-xs font-medium hover:bg-hover"
                :title="`Downloads the current contents of ${v.name} as a tar archive`"
              >
                <UiIcon name="download" :size="12" />
                Download backup
              </a>
              <UiButton size="sm" :disabled="props.busy || !props.stopped" :title="restoreTitle" @click="emit('restore', v)">
                <UiIcon name="upload" :size="12" />
                Restore from backup
              </UiButton>
            </span>
          </td>
        </tr>
      </tbody>
    </table>
    <p class="border-t border-line px-4 py-2 text-xs text-fg-subtle">
      <template v-if="props.admin">
        A backup of a running database is not guaranteed consistent; prefer its own dump tool for that. A restore needs the application stopped and replaces everything in the volume.
      </template>
      <template v-else>
        Backups and restores need the admin role; this token cannot take or restore one.
      </template>
    </p>
  </div>
</template>
