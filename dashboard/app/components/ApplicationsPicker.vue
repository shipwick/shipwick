<script setup lang="ts">
import { parseApplications } from '~/utils/access'

/**
 * The applications a deploy token, or a rule, is limited to: the existing
 * ones to tick, and a field for names that do not exist yet — a first
 * deployment is covered by its name. Nothing chosen means every application.
 */
const props = defineProps<{
  /** The chosen names. */
  modelValue: string[]
  /** The applications on the server, to choose from. */
  known: string[]
  /** For the label and the ids of the fields. */
  id: string
  disabled?: boolean
  /** Why it is off, said under it. */
  disabledReason?: string
}>()

const emit = defineEmits<{ 'update:modelValue': [names: string[]] }>()

const extra = ref('')
const problem = ref('')

/** Chosen names that are not among the server's applications: added by hand. */
const others = computed(() => props.modelValue.filter(n => !props.known.includes(n)))

function toggle(name: string) {
  const next = props.modelValue.includes(name) ? props.modelValue.filter(n => n !== name) : [...props.modelValue, name]
  emit('update:modelValue', next.sort())
}

function addExtra() {
  const parsed = parseApplications(extra.value)
  problem.value = parsed.problem
  if (parsed.problem || parsed.names.length === 0) return
  emit('update:modelValue', [...new Set([...props.modelValue, ...parsed.names])].sort())
  extra.value = ''
}

const CHIP = 'inline-flex h-6 items-center gap-1 rounded-sm border px-2 text-xs transition-colors duration-100'
</script>

<template>
  <fieldset :disabled="props.disabled" class="min-w-0">
    <legend class="label">
      Applications
    </legend>
    <template v-if="!props.disabled">
      <div v-if="props.known.length > 0 || others.length > 0" class="mt-1.5 flex flex-wrap gap-1.5">
        <button
          v-for="name in [...props.known, ...others]"
          :key="name"
          type="button"
          class="mono"
          :class="[CHIP, props.modelValue.includes(name) ? 'border-fg bg-active font-medium text-fg' : 'border-line-strong text-fg-muted hover:bg-hover hover:text-fg']"
          :aria-pressed="props.modelValue.includes(name)"
          @click="toggle(name)"
        >
          <UiIcon v-if="props.modelValue.includes(name)" name="check" :size="10" />
          {{ name }}
        </button>
      </div>
      <div class="mt-1.5 flex gap-2">
        <label :for="`${props.id}-extra`" class="sr-only">Another application, by name</label>
        <input
          :id="`${props.id}-extra`"
          v-model="extra"
          type="text"
          class="input mono !h-7 min-w-0 flex-1 !text-xs"
          placeholder="a name that is not deployed yet"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          :aria-invalid="problem !== '' || undefined"
          @keydown.enter.prevent="addExtra"
        >
        <UiButton size="sm" :disabled="extra.trim() === ''" @click="addExtra">
          Add
        </UiButton>
      </div>
    </template>
    <p class="mt-1.5 text-xs" :class="problem ? 'text-danger' : 'text-fg-muted'">
      <template v-if="problem">
        {{ problem }}
      </template>
      <template v-else-if="props.disabled">
        {{ props.disabledReason }}
      </template>
      <template v-else-if="props.modelValue.length === 0">
        None chosen: every application, also ones deployed later.
      </template>
      <template v-else>
        Only {{ props.modelValue.length === 1 ? 'this one' : `these ${props.modelValue.length}` }} can be deployed, stopped and started; everything else can be looked at.
      </template>
    </p>
  </fieldset>
</template>
