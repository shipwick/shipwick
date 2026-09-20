<script setup lang="ts">
const props = defineProps<{ title: string }>()
</script>

<template>
  <div class="px-4 py-8 sm:px-6">
    <p class="font-medium">
      {{ props.title }}
    </p>
    <div v-if="$slots.default" class="mt-1 max-w-prose text-fg-muted">
      <slot />
    </div>
  </div>
</template>
