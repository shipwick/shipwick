<script setup lang="ts">
import type { RunDetail, RunRequest } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { argvFromFields } from '~/utils/jobs'
import { formatArgv } from '~/utils/spec'

/**
 * Runs a one-off command in a container from the application's image, with
 * its environment. The API takes an argv and never a shell string, so the
 * editor has one field per argument: nothing typed here is split or quoted.
 */
const props = defineProps<{ open: boolean, application: string, image: string }>()
const emit = defineEmits<{ close: [], started: [run: RunDetail] }>()

const agent = useAgent()
const fields = ref<string[]>([''])
const pending = ref(false)
const error = shallowRef<AgentError | null>(null)
const inputs = ref<HTMLInputElement[]>([])

watch(() => props.open, (open) => {
  if (!open) return
  fields.value = ['']
  error.value = null
})

const argv = computed(() => argvFromFields(fields.value))

function add() {
  fields.value = [...fields.value, '']
  void nextTick(() => inputs.value[fields.value.length - 1]?.focus())
}

function remove(index: number) {
  if (fields.value.length === 1) {
    fields.value = ['']
    return
  }
  fields.value = fields.value.filter((_, i) => i !== index)
}

function update(index: number, value: string) {
  fields.value = fields.value.map((f, i) => (i === index ? value : f))
}

// Enter in the last field adds the next argument; Enter elsewhere submits.
function onEnter(index: number) {
  if (index === fields.value.length - 1 && fields.value[index] !== '') add()
  else void submit()
}

async function submit() {
  if (pending.value || argv.value.length === 0) return
  pending.value = true
  error.value = null
  try {
    const body: RunRequest = { command: argv.value }
    const run = await agent.post<RunDetail>(`/applications/${encodeURIComponent(props.application)}/run`, { body })
    emit('started', run)
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
  <UiDialog :open="props.open" :title="`Run a command in ${props.application}`" size="md" :busy="pending" @close="emit('close')">
    <form id="run-command-form" class="space-y-4" @submit.prevent="submit">
      <p class="text-fg-muted">
        Runs once in a new container from <span class="mono text-fg">{{ props.image }}</span> with the application's environment and limits, and no volume. One argument per field: the agent receives them as a list, never as a shell line, so <span class="mono text-fg">a b</span> in one field is a single argument.
      </p>
      <fieldset>
        <legend class="label mb-1.5">
          Command
        </legend>
        <ol class="space-y-1.5">
          <li v-for="(field, index) in fields" :key="index" class="flex items-center gap-2">
            <span class="mono w-5 shrink-0 text-right text-xs text-fg-subtle">{{ index }}</span>
            <input
              ref="inputs"
              :value="field"
              type="text"
              class="input mono"
              :placeholder="index === 0 ? 'rails' : index === 1 ? 'db:migrate' : ''"
              :aria-label="`Argument ${index}`"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              :autofocus="index === 0"
              @input="update(index, ($event.target as HTMLInputElement).value)"
              @keydown.enter.prevent="onEnter(index)"
            >
            <button
              type="button"
              class="target flex items-center justify-center rounded-sm p-1.5 text-fg-subtle hover:bg-hover hover:text-fg"
              :aria-label="`Remove argument ${index}`"
              :disabled="fields.length === 1 && field === ''"
              @click="remove(index)"
            >
              <UiIcon name="close" :size="12" />
            </button>
          </li>
        </ol>
        <UiButton size="sm" variant="ghost" class="mt-2" @click="add">
          Add argument
        </UiButton>
      </fieldset>
      <p class="mono break-all rounded-sm border border-line bg-inset px-3 py-2 text-xs" :class="argv.length ? 'text-fg' : 'text-fg-subtle'">
        {{ argv.length ? formatArgv(argv) : 'Nothing to run yet' }}
      </p>
      <p class="text-xs text-fg-muted">
        Several commands may run at once. The run is followed here until it is over; its last 200 lines of output are kept.
      </p>
      <InlineError :error="error" />
    </form>
    <template #footer>
      <UiButton :disabled="pending" @click="emit('close')">
        Cancel
      </UiButton>
      <UiButton type="submit" form="run-command-form" variant="primary" :pending="pending" :disabled="argv.length === 0">
        Run
      </UiButton>
    </template>
  </UiDialog>
</template>
