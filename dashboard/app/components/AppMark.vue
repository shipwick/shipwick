<script setup lang="ts">
// Wick, the Shipwick flame. The paths are the ones shipped at shipwick.com
// (img/wick.svg and favicon-wick.svg); change them there first. The colours
// are Wick's own and stay the same in both themes, so nothing here uses
// currentColor.
//
// `compact` draws the head alone, framed like the favicon: at 16-20 px the
// full pose's feet are a smudge, and the mark then matches the browser tab.
const props = withDefaults(defineProps<{ size?: number, compact?: boolean }>(), { size: 18, compact: false })
</script>

<template>
  <svg
    v-if="props.compact"
    :width="props.size"
    :height="props.size"
    viewBox="0 0 32 32"
    fill="none"
    aria-hidden="true"
    class="shrink-0"
  >
    <g transform="scale(.25)">
      <path d="M74 8C80 24 100 50 100 78C100 100 86 116 64 116C42 116 28 100 28 78C28 64 30 50 36 42C38 39 41 40 44 46C48 54 52 56 56 54C62 48 68 30 74 8Z" fill="#0b62d6" />
      <path d="M70 46C74 58 86 70 86 88C86 102 76 110 64 110C52 110 42 102 42 88C42 74 50 68 56 60C62 54 66 52 70 46Z" fill="#5b9dff" />
    </g>
    <ellipse cx="13.2" cy="20.5" rx="2.5" ry="2.9" fill="#ffffff" />
    <ellipse cx="18.8" cy="20.5" rx="2.5" ry="2.9" fill="#ffffff" />
    <circle cx="13.4" cy="20.9" r="1.3" fill="#0f172a" />
    <circle cx="19" cy="20.9" r="1.3" fill="#0f172a" />
    <path d="M14.3 24.6Q16 26.1 17.7 24.6" stroke="#0f172a" stroke-width="1" stroke-linecap="round" fill="none" />
  </svg>
  <svg
    v-else
    :width="props.size"
    :height="props.size"
    viewBox="0 0 128 128"
    fill="none"
    aria-hidden="true"
    class="shrink-0"
  >
    <path d="M36 92L28 106" stroke="#0b62d6" stroke-width="7" stroke-linecap="round" />
    <path d="M92 92L100 106" stroke="#0b62d6" stroke-width="7" stroke-linecap="round" />
    <path d="M74 8C80 24 100 50 100 78C100 100 86 116 64 116C42 116 28 100 28 78C28 64 30 50 36 42C38 39 41 40 44 46C48 54 52 56 56 54C62 48 68 30 74 8Z" fill="#0b62d6" />
    <path d="M70 46C74 58 86 70 86 88C86 102 76 110 64 110C52 110 42 102 42 88C42 74 50 68 56 60C62 54 66 52 70 46Z" fill="#5b9dff" />
    <ellipse cx="53" cy="80" rx="7" ry="8" fill="#ffffff" />
    <ellipse cx="75" cy="80" rx="7" ry="8" fill="#ffffff" />
    <circle cx="53.5" cy="81" r="3.4" fill="#0f172a" />
    <circle cx="75.5" cy="81" r="3.4" fill="#0f172a" />
    <path d="M58 95Q64 100 70 95" stroke="#0f172a" stroke-width="2.4" stroke-linecap="round" fill="none" />
  </svg>
</template>
