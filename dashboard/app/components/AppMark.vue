<script setup lang="ts">
const props = withDefaults(defineProps<{ size?: number }>(), { size: 18 })
</script>

<template>
  <!-- Two stacked containers, the upper one shifted: a deployment replacing another. -->
  <svg :width="props.size" :height="props.size" viewBox="0 0 18 18" fill="none" aria-hidden="true" class="shrink-0">
    <rect x="1" y="10" width="12" height="7" rx="1" fill="currentColor" opacity="0.35" />
    <rect x="5" y="1" width="12" height="7" rx="1" fill="currentColor" />
  </svg>
</template>
