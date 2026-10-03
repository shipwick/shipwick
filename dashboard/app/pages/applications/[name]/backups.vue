<script setup lang="ts">
import type { SpecVolume } from '~/types/api'

/**
 * The application's data: the backups the agent takes of its volumes, and the
 * volumes themselves with a download and a restore from a file.
 */
const { name, spec, gone, hasActive, busy, stoppedNow, mayDeploy, mayAdmin, refreshAll, setRunning } = useApplication()

const volumes = computed(() => spec.value?.volumes ?? [])

const restoring = ref<SpecVolume | null>(null)

/** The restore dialog offers to start the application; it stays stopped otherwise. */
function startAfterRestore() {
  restoring.value = null
  void setRunning(true)
}
</script>

<template>
  <div v-if="volumes.length === 0" class="rounded-sm border border-line">
    <EmptyState title="Nothing to back up">
      A backup is an archive of an application's volumes, and this one has none. Give it some under <span class="mono text-fg">volumes</span> in deploy.yaml.
    </EmptyState>
  </div>
  <div v-else class="space-y-8">
    <BackupsPanel
      v-if="spec && hasActive && !gone"
      :application="name"
      :spec="spec"
      :may-deploy="mayDeploy"
      :admin="mayAdmin"
      :stopped="stoppedNow"
      :busy="busy"
      @changed="refreshAll"
    />

    <UiPanel title="Volumes" :meta="volumes.length">
      <VolumesPanel
        :application="name"
        :volumes="volumes"
        :admin="mayAdmin"
        :stopped="stoppedNow"
        :busy="busy"
        @restore="restoring = $event"
      />
    </UiPanel>

    <RestoreDialog
      :open="restoring !== null"
      :application="name"
      :volume="restoring"
      @close="restoring = null"
      @restored="refreshAll"
      @start="startAfterRestore"
    />
  </div>
</template>
