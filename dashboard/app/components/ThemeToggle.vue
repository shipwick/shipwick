<script setup lang="ts">
import type { ThemePreference } from '~/composables/useTheme'
import type { IconName } from '~/components/UiIcon.vue'
import { rovingTabStop } from '~/utils/focus'

const { preference, set } = useTheme()

const OPTIONS: { value: ThemePreference, label: string, icon: IconName }[] = [
  { value: 'system', label: 'System theme', icon: 'monitor' },
  { value: 'light', label: 'Light theme', icon: 'sun' },
  { value: 'dark', label: 'Dark theme', icon: 'moon' },
]

// A radio group is one stop: the arrows move between the themes and choose, as they do between native radios.
const group = ref<HTMLElement | null>(null)
const tabStop = computed(() => rovingTabStop(OPTIONS.length, OPTIONS.findIndex(option => option.value === preference.value)))
const { onKeydown } = useRovingFocus(group, { selector: '[role="radio"]', onMove: index => set(OPTIONS[index]!.value) })
</script>

<template>
  <div ref="group" role="radiogroup" aria-label="Theme" class="inline-flex rounded-sm border border-line p-0.5" @keydown="onKeydown">
    <button
      v-for="(option, index) in OPTIONS"
      :key="option.value"
      type="button"
      role="radio"
      :aria-checked="preference === option.value"
      :aria-label="option.label"
      :title="option.label"
      :tabindex="index === tabStop ? 0 : -1"
      class="target flex size-6 items-center justify-center rounded-xs transition-colors duration-100"
      :class="preference === option.value ? 'bg-active text-fg' : 'text-fg-subtle hover:text-fg'"
      @click="set(option.value)"
    >
      <UiIcon :name="option.icon" :size="14" />
    </button>
  </div>
</template>
