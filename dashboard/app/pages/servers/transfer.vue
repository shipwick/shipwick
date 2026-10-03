<script setup lang="ts">
/** Moving to another server, and a second server kept ready: exports, the last import, the standby. */
const polling = useServerInfo()
const access = useAccess()
</script>

<template>
  <div class="space-y-8">
    <TransferPanels :admin="access.can('admin')" @changed="polling.refresh()" />
    <p v-if="!access.can('admin')" class="rounded-sm border border-line px-4 py-3 text-fg-muted">
      Exports and imports are shown to admin tokens only: an export carries every application's secrets.
    </p>
  </div>
</template>
