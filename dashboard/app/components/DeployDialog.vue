<script setup lang="ts">
import type { ApplicationDetail, Deployment, RedeployRequest } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { splitImage } from '~/utils/format'

/**
 * Deploy = re-deploy the active spec, optionally with another image. The spec
 * itself stays on the server: the API only ever shows env values masked, so
 * the dashboard could not re-submit it even if it wanted to.
 */
const props = defineProps<{ open: boolean, application: ApplicationDetail }>()
const emit = defineEmits<{ close: [], started: [deployment: Deployment] }>()

const agent = useAgent()
const image = ref('')
const pending = ref(false)
const error = shallowRef<AgentError | null>(null)

const input = ref<HTMLInputElement | null>(null)

watch(() => props.open, async (open) => {
  if (!open) return
  image.value = props.application.image
  error.value = null
  // The usual edit is "same image, next tag": preselect the tag so typing replaces it.
  await nextTick()
  const { repository, tag } = splitImage(image.value)
  if (tag && input.value) input.value.setSelectionRange(repository.length + 1, repository.length + 1 + tag.length)
})

const trimmed = computed(() => image.value.trim())
const unchanged = computed(() => trimmed.value === props.application.image)
const nextVersion = computed(() => splitImage(trimmed.value).tag || 'latest')

async function submit() {
  if (pending.value || trimmed.value === '') return
  pending.value = true
  error.value = null
  try {
    const body: RedeployRequest = unchanged.value ? {} : { image: trimmed.value }
    const deployment = await agent.post<Deployment>(`/applications/${encodeURIComponent(props.application.name)}/redeploy`, { body })
    emit('started', deployment)
    emit('close')
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
  <UiDialog :open="props.open" :title="`Deploy ${props.application.name}`" :busy="pending" @close="emit('close')">
    <form id="deploy-form" class="space-y-4" @submit.prevent="submit">
      <div>
        <label for="deploy-image" class="label block">Image</label>
        <input
          id="deploy-image"
          ref="input"
          v-model="image"
          type="text"
          class="input mono mt-1.5"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          autofocus
          required
          :aria-invalid="error?.code === 'INVALID_CONFIG' || undefined"
        >
        <p class="mt-1.5 text-xs text-fg-muted">
          <template v-if="unchanged">
            Re-deploys <span class="mono text-fg">{{ props.application.version || 'the current image' }}</span> with the current configuration: the image is pulled again and all replicas are replaced.
          </template>
          <template v-else>
            Deploys version <span class="mono text-fg">{{ nextVersion }}</span> with the current configuration.
          </template>
        </p>
      </div>
      <p class="text-xs text-fg-muted">
        The running version keeps serving until the new replicas are healthy. To change anything other than the image, use <span class="mono text-fg">shipwick deploy</span>.
      </p>
      <InlineError :error="error" />
    </form>
    <template #footer>
      <UiButton :disabled="pending" @click="emit('close')">
        Cancel
      </UiButton>
      <UiButton type="submit" form="deploy-form" variant="primary" :pending="pending" :disabled="trimmed === ''">
        Deploy
      </UiButton>
    </template>
  </UiDialog>
</template>
