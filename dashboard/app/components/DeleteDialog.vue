<script setup lang="ts">
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'

const props = defineProps<{ open: boolean, name: string }>()
const emit = defineEmits<{ close: [], deleted: [] }>()

const agent = useAgent()
const confirmation = ref('')
const pending = ref(false)
const error = shallowRef<AgentError | null>(null)

watch(() => props.open, (open) => {
  if (!open) return
  confirmation.value = ''
  error.value = null
})

const confirmed = computed(() => confirmation.value === props.name)

async function submit() {
  if (pending.value || !confirmed.value) return
  pending.value = true
  error.value = null
  try {
    await agent.del(`/applications/${encodeURIComponent(props.name)}`)
    emit('deleted')
  }
  catch (cause) {
    error.value = toAgentError(cause)
  }
  finally {
    pending.value = false
  }
}
</script>

<template>
  <UiDialog :open="props.open" :title="`Delete ${props.name}`" :busy="pending" @close="emit('close')">
    <form id="delete-form" class="space-y-4" @submit.prevent="submit">
      <p class="text-fg-muted">
        This stops and removes every container of <span class="mono text-fg">{{ props.name }}</span> and deletes its deployment history and events. It cannot be undone, and there is nothing left to roll back to.
      </p>
      <div>
        <label for="delete-confirm" class="block text-fg-muted">
          Type <span class="mono select-all font-medium text-fg">{{ props.name }}</span> to confirm
        </label>
        <input
          id="delete-confirm"
          v-model="confirmation"
          type="text"
          class="input mono mt-1.5"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          autofocus
        >
      </div>
      <InlineError :error="error" />
    </form>
    <template #footer>
      <UiButton :disabled="pending" @click="emit('close')">
        Cancel
      </UiButton>
      <UiButton type="submit" form="delete-form" variant="danger-solid" :pending="pending" :disabled="!confirmed">
        Delete application
      </UiButton>
    </template>
  </UiDialog>
</template>
