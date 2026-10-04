<script setup lang="ts">
import type { Tone } from '~/utils/status'

/** Status is always a dot plus a text label: never color alone. */
const props = withDefaults(defineProps<{
  tone: Tone
  label: string
  /** The raw API value, where the label paraphrases it: shown to the pointer, and nothing a screen reader has not read. */
  raw?: string
  /** What the label does not say — why it failed, what it means: a tooltip the keyboard reaches too. */
  detail?: string
  size?: 'sm' | 'md'
}>(), { raw: undefined, detail: undefined, size: 'sm' })

const DOT: Record<Tone, string> = {
  ok: 'bg-ok-dot',
  warn: 'bg-warn-dot',
  danger: 'bg-danger-dot',
  muted: 'bg-muted-dot',
}

const TEXT: Record<Tone, string> = {
  ok: 'text-ok',
  warn: 'text-warn',
  danger: 'text-danger',
  muted: 'text-fg-muted',
}
</script>

<template>
  <UiTooltip
    :text="props.detail || props.raw"
    :repeats="!props.detail"
    class="inline-flex items-center whitespace-nowrap font-medium"
    :class="[TEXT[props.tone], props.size === 'md' ? 'gap-2 text-base' : 'gap-1.5 text-sm']"
  >
    <span class="inline-block shrink-0 rounded-full" :class="[DOT[props.tone], props.size === 'md' ? 'size-2' : 'size-1.5']" aria-hidden="true" />
    {{ props.label }}
  </UiTooltip>
</template>
