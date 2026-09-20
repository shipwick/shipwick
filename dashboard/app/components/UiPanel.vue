<script setup lang="ts">
/** A titled section: micro-label header, optional actions, hairline box around the content. */
const props = withDefaults(defineProps<{
  title: string
  /** A count or short fact shown next to the title. */
  meta?: string | number | null
  /** Set false for content that brings its own box or needs none. */
  bordered?: boolean
}>(), { meta: null, bordered: true })
</script>

<template>
  <section class="min-w-0">
    <header class="mb-2 flex min-h-6 items-center justify-between gap-3">
      <h2 class="label flex items-center gap-2">
        {{ props.title }}
        <span v-if="props.meta !== null && props.meta !== ''" class="mono font-normal tracking-normal text-fg-faint">{{ props.meta }}</span>
      </h2>
      <div v-if="$slots.actions" class="flex items-center gap-2">
        <slot name="actions" />
      </div>
    </header>
    <div :class="props.bordered ? 'overflow-hidden rounded-sm border border-line bg-bg' : ''">
      <slot />
    </div>
  </section>
</template>
