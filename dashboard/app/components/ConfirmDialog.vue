<script setup lang="ts">
import type { AgentError } from '~/utils/agentError'

/** A plain "are you sure" for actions that interrupt service but lose nothing. */
const props = withDefaults(defineProps<{
  open: boolean
  title: string
  confirmLabel: string
  pending?: boolean
  error?: AgentError | null
  danger?: boolean
}>(), { pending: false, error: null, danger: false })

const emit = defineEmits<{ close: [], confirm: [] }>()
</script>

<template>
  <UiDialog :open="props.open" :title="props.title" :busy="props.pending" @close="emit('close')">
    <div class="space-y-4">
      <div class="text-fg-muted">
        <slot />
      </div>
      <InlineError :error="props.error" />
    </div>
    <template #footer>
      <UiButton :disabled="props.pending" autofocus @click="emit('close')">
        Cancel
      </UiButton>
      <UiButton :variant="props.danger ? 'danger-solid' : 'primary'" :pending="props.pending" @click="emit('confirm')">
        {{ props.confirmLabel }}
      </UiButton>
    </template>
  </UiDialog>
</template>
