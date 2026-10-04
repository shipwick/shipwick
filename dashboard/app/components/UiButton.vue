<script setup lang="ts">
import type { RouteLocationRaw } from 'vue-router'
import { buttonMode, hasText } from '~/utils/tooltip'

defineOptions({ inheritAttrs: false })

const props = withDefaults(defineProps<{
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger' | 'danger-solid'
  size?: 'sm' | 'md'
  type?: 'button' | 'submit'
  /** Shows a spinner and blocks clicks, without changing the button's width. */
  pending?: boolean
  /** With a `hint` that says why, the button stays reachable by Tab so the reason can be read. */
  disabled?: boolean
  /** What the button does, or why it is off: its tooltip, and its description for a screen reader. */
  hint?: string | null
  /** Renders a link that looks like a button. */
  to?: RouteLocationRaw
}>(), {
  variant: 'secondary',
  size: 'md',
  type: 'button',
  pending: false,
  disabled: false,
  hint: undefined,
  to: undefined,
})

const attrs = useAttrs()

const VARIANTS: Record<NonNullable<typeof props.variant>, string> = {
  'primary': 'bg-primary text-primary-fg border-primary',
  'secondary': 'bg-bg text-fg border-line-strong',
  'ghost': 'bg-transparent text-fg-muted border-transparent',
  // Destructive, but quiet until it is the confirming action of a dialog.
  'danger': 'bg-bg text-danger border-line-strong',
  'danger-solid': 'bg-danger-solid text-[#fff] border-danger-solid',
}

const HOVER: Record<NonNullable<typeof props.variant>, string> = {
  'primary': 'hover:bg-primary-hover hover:border-primary-hover',
  'secondary': 'hover:bg-hover',
  'ghost': 'hover:bg-hover hover:text-fg',
  'danger': 'hover:border-danger-line hover:bg-danger-bg',
  'danger-solid': 'hover:bg-danger-solid-hover hover:border-danger-solid-hover',
}

/**
 * Off, but still a stop of the Tab order. A native `disabled` drops the focus
 * the moment it is set: a button that turns busy when it is pressed would
 * leave a keyboard user at the top of the page, and one that is off for a
 * reason could never tell it. Such a button is marked aria-disabled instead
 * and its clicks are swallowed.
 */
const mode = computed(() => buttonMode(props.disabled, props.pending, props.hint))
const inert = computed(() => mode.value === 'inert')

const classes = computed(() => [
  'target relative inline-flex select-none items-center justify-center gap-1.5 whitespace-nowrap rounded-sm border font-medium transition-colors duration-100',
  props.size === 'sm' ? 'h-7 px-2.5 text-xs' : 'h-8 px-3 text-sm',
  VARIANTS[props.variant],
  // Off, it answers the pointer with nothing but its hint, if it has one.
  mode.value === 'enabled' ? HOVER[props.variant] : hasText(props.hint) ? 'cursor-default' : 'pointer-events-none',
  props.disabled && !props.pending ? 'opacity-45' : '',
])

const passed = computed(() => {
  if (!inert.value) return attrs
  const { onClick: _, ...rest } = attrs
  return rest
})

// Also what Enter in a field of the button's form arrives as: a click on its submit button.
function swallow(event: Event) {
  if (inert.value) event.preventDefault()
}
</script>

<template>
  <UiTooltip v-if="props.to" :to="props.to" :text="props.hint" :class="classes" v-bind="attrs">
    <slot />
  </UiTooltip>
  <UiTooltip
    v-else
    as="button"
    :text="props.hint"
    v-bind="passed"
    :type="props.type"
    :class="classes"
    :disabled="mode === 'disabled' || undefined"
    :aria-disabled="inert || undefined"
    :aria-busy="props.pending || undefined"
    @click="swallow"
  >
    <span class="inline-flex items-center gap-1.5" :class="props.pending ? 'opacity-0' : ''"><slot /></span>
    <span v-if="props.pending" class="absolute inset-0 flex items-center justify-center" aria-hidden="true">
      <svg class="spinner" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round">
        <path d="M8 2a6 6 0 1 1-6 6" />
      </svg>
    </span>
  </UiTooltip>
</template>
