<script setup lang="ts">
/**
 * The two live regions useAnnounce speaks through. They are in the document
 * from the start: a region added together with its text is often not read.
 */
const { polite, assertive } = useAnnounce()
</script>

<template>
  <div class="sr-only">
    <p role="status" aria-live="polite" aria-atomic="true">
      {{ polite }}
    </p>
    <p role="alert" aria-live="assertive" aria-atomic="true">
      {{ assertive }}
    </p>
  </div>
</template>
