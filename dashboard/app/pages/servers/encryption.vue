<script setup lang="ts">
/** The key the agent encrypts stored values with, and replacing it. */
const access = useAccess()
</script>

<template>
  <div>
    <KeyRotationPanel :admin="access.can('admin')" />
  </div>
</template>
