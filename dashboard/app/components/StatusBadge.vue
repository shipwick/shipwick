<script setup lang="ts">
import type { Tone } from '~/utils/status'

/** Status is always a dot plus a text label: never color alone. */
const props = withDefaults(defineProps<{
  tone: Tone
  label: string
  /** The raw API value, shown as a tooltip when the label paraphrases it. */
  raw?: string
  size?: 'sm' | 'md'
}>(), { raw: undefined, size: 'sm' })

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
  <span
    class="inline-flex items-center whitespace-nowrap font-medium"
    :class="[TEXT[props.tone], props.size === 'md' ? 'gap-2 text-base' : 'gap-1.5 text-sm']"
    :title="props.raw"
  >
    <span class="inline-block shrink-0 rounded-full" :class="[DOT[props.tone], props.size === 'md' ? 'size-2' : 'size-1.5']" aria-hidden="true" />
    {{ props.label }}
  </span>
</template>
