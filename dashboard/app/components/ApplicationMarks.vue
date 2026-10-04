<script setup lang="ts">
import type { Application } from '~/types/api'
import { applicationMarks } from '~/utils/marks'

/** Next to an application's status in a list: a certificate that is not in order, and its alerts. Nothing for an application with neither. */
const props = defineProps<{ application: Pick<Application, 'certificate_problem' | 'alert_count' | 'alert_severity'> }>()

const marks = computed(() => applicationMarks(props.application))

const FRAME = {
  warn: 'border-warn-line bg-warn-bg text-warn',
  danger: 'border-danger-line bg-danger-bg text-danger',
  ok: 'border-line bg-subtle text-fg-muted',
  muted: 'border-line bg-subtle text-fg-muted',
} as const
</script>

<template>
  <UiTooltip
    v-for="mark in marks"
    :key="mark.key"
    :text="mark.title"
    class="inline-flex h-5 items-center gap-1 whitespace-nowrap rounded-sm border px-1.5 text-2xs font-medium"
    :class="FRAME[mark.tone]"
  >
    <UiIcon :name="mark.key === 'certificate' ? 'certificate' : 'alert'" :size="10" />
    {{ mark.label }}
  </UiTooltip>
</template>
