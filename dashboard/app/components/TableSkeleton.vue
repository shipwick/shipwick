<script setup lang="ts">
/** Placeholder rows with the same rhythm as a data table, so content does not jump in. */
const props = withDefaults(defineProps<{ rows?: number, columns?: number }>(), { rows: 5, columns: 5 })

const WIDTHS = ['w-24', 'w-16', 'w-32', 'w-12', 'w-20', 'w-28']
</script>

<template>
  <div aria-busy="true" aria-label="Loading">
    <div class="h-[33px] border-b border-line bg-subtle" />
    <div v-for="row in props.rows" :key="row" class="flex h-[39px] items-center gap-6 border-b border-line px-4 last:border-b-0">
      <span
        v-for="column in props.columns"
        :key="column"
        class="skeleton"
        :class="[WIDTHS[(row + column) % WIDTHS.length], column > 2 ? 'hidden sm:block' : '']"
      />
    </div>
  </div>
</template>
