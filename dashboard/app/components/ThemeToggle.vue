<script setup lang="ts">
import type { ThemePreference } from '~/composables/useTheme'
import type { IconName } from '~/components/UiIcon.vue'

const { preference, set } = useTheme()

const OPTIONS: { value: ThemePreference, label: string, icon: IconName }[] = [
  { value: 'system', label: 'System theme', icon: 'monitor' },
  { value: 'light', label: 'Light theme', icon: 'sun' },
  { value: 'dark', label: 'Dark theme', icon: 'moon' },
]
</script>

<template>
  <div role="radiogroup" aria-label="Theme" class="inline-flex rounded-sm border border-line p-0.5">
    <button
      v-for="option in OPTIONS"
      :key="option.value"
      type="button"
      role="radio"
      :aria-checked="preference === option.value"
      :aria-label="option.label"
      :title="option.label"
      class="flex size-6 items-center justify-center rounded-xs transition-colors duration-100"
      :class="preference === option.value ? 'bg-active text-fg' : 'text-fg-subtle hover:text-fg'"
      @click="set(option.value)"
    >
      <UiIcon :name="option.icon" :size="14" />
    </button>
  </div>
</template>
