<script setup lang="ts">
import type { RouteLocationRaw } from 'vue-router'

const props = withDefaults(defineProps<{
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger' | 'danger-solid'
  size?: 'sm' | 'md'
  type?: 'button' | 'submit'
  /** Shows a spinner and blocks clicks, without changing the button's width. */
  pending?: boolean
  disabled?: boolean
  /** Renders a link that looks like a button. */
  to?: RouteLocationRaw
}>(), {
  variant: 'secondary',
  size: 'md',
  type: 'button',
  pending: false,
  disabled: false,
  to: undefined,
})

const VARIANTS: Record<NonNullable<typeof props.variant>, string> = {
  'primary': 'bg-primary text-primary-fg border-primary hover:bg-primary-hover hover:border-primary-hover',
  'secondary': 'bg-bg text-fg border-line-strong hover:bg-hover',
  'ghost': 'bg-transparent text-fg-muted border-transparent hover:bg-hover hover:text-fg',
  // Destructive, but quiet until it is the confirming action of a dialog.
  'danger': 'bg-bg text-danger border-line-strong hover:border-danger-line hover:bg-danger-bg',
  'danger-solid': 'bg-danger-solid text-[#fff] border-danger-solid hover:bg-danger-solid-hover hover:border-danger-solid-hover',
}

const classes = computed(() => [
  'relative inline-flex select-none items-center justify-center gap-1.5 whitespace-nowrap rounded-sm border font-medium transition-colors duration-100',
  props.size === 'sm' ? 'h-7 px-2.5 text-xs' : 'h-8 px-3 text-sm',
  VARIANTS[props.variant],
  (props.disabled || props.pending) ? 'pointer-events-none' : '',
  props.disabled && !props.pending ? 'opacity-45' : '',
])
</script>

<template>
  <NuxtLink v-if="props.to" :to="props.to" :class="classes">
    <slot />
  </NuxtLink>
  <button
    v-else
    :type="props.type"
    :class="classes"
    :disabled="props.disabled || props.pending"
    :aria-busy="props.pending || undefined"
  >
    <span class="inline-flex items-center gap-1.5" :class="props.pending ? 'invisible' : ''"><slot /></span>
    <span v-if="props.pending" class="absolute inset-0 flex items-center justify-center" aria-hidden="true">
      <svg class="spinner" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round">
        <path d="M8 2a6 6 0 1 1-6 6" />
      </svg>
    </span>
  </button>
</template>
