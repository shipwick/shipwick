<script setup lang="ts">
import type { Finding } from '~/utils/diagnosis'

/** What is wrong and where to look, framed like the agent's alerts so the two read as one list. */
const props = defineProps<{ findings: Finding[] }>()

const FRAME = {
  warn: 'border-warn-line bg-warn-bg text-warn',
  danger: 'border-danger-line bg-danger-bg text-danger',
  ok: 'border-line bg-subtle text-fg',
  muted: 'border-line bg-subtle text-fg-muted',
} as const
</script>

<template>
  <ul v-if="props.findings.length > 0" class="space-y-2" aria-label="What needs attention">
    <li
      v-for="finding in props.findings"
      :key="finding.key"
      class="flex flex-wrap items-start gap-x-3 gap-y-1.5 rounded-sm border px-3 py-2 text-xs"
      :class="FRAME[finding.tone]"
    >
      <UiIcon :name="finding.tone === 'danger' ? 'x-circle' : 'alert'" :size="14" class="mt-px" />
      <div class="min-w-0 flex-1 basis-64">
        <p class="font-medium">
          {{ finding.title }}
        </p>
        <p v-if="finding.detail" class="mt-0.5 break-words">
          {{ finding.detail }}
        </p>
      </div>
      <NuxtLink v-if="finding.action" :to="finding.action.to" class="notice-action">
        {{ finding.action.label }}
        <UiIcon name="chevron-right" :size="12" />
      </NuxtLink>
    </li>
  </ul>
</template>
