<script setup lang="ts">
import { formatAbsoluteUtc, formatRelativeTime } from '~/utils/format'

/** "2m ago", with the absolute UTC time as a tooltip and as machine-readable datetime. */
const props = withDefaults(defineProps<{ time: string | null | undefined, prefix?: string }>(), { prefix: '' })

const now = useNow()
const relative = computed(() => formatRelativeTime(props.time, now.value))
const absolute = computed(() => formatAbsoluteUtc(props.time))
</script>

<template>
  <time v-if="props.time" :datetime="props.time" :title="absolute" class="mono tight whitespace-nowrap">{{ props.prefix }}{{ relative }}</time>
  <span v-else class="text-fg-faint">—</span>
</template>
