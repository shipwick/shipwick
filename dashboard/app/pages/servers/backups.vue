<script setup lang="ts">
/** Where backups go, and the backups of the agent's own data. */
const polling = useServerInfo()
const access = useAccess()
</script>

<template>
  <div>
    <StateBackups v-if="polling.data.value?.backups" :status="polling.data.value.backups" :admin="access.can('admin')" @changed="polling.refresh()" />
    <div v-else class="rounded-sm border border-line">
      <EmptyState title="This agent takes no backups by itself">
        Scheduled backups, and the backup of the agent's own data, need a newer agent. Upgrade it; until then a volume is downloaded by hand on its application's page.
      </EmptyState>
    </div>
  </div>
</template>
