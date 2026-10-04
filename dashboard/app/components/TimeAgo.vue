<script setup lang="ts">
import { formatAbsoluteUtc, formatRelativeTime } from '~/utils/format'

/**
 * "2m ago", with the absolute UTC time as a tooltip and as machine-readable
 * datetime. `plain` where the absolute time is said next to it — beside it,
 * or by the control of its row: the tooltip is then the pointer's alone, and
 * not one more stop of the Tab order.
 */
const props = withDefaults(defineProps<{ time: string | null | undefined, prefix?: string, plain?: boolean }>(), { prefix: '', plain: false })

const now = useNow()
const relative = computed(() => formatRelativeTime(props.time, now.value))
const absolute = computed(() => formatAbsoluteUtc(props.time))
</script>

<template>
  <UiTooltip v-if="props.time" as="time" :datetime="props.time" :text="absolute" :repeats="props.plain" class="mono tight whitespace-nowrap">{{ props.prefix }}{{ relative }}</UiTooltip>
  <span v-else class="text-fg-subtle">—</span>
</template>
