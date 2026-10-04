<script setup lang="ts">
import type { RouteLocationRaw } from 'vue-router'
import { hasText, needsTabStop } from '~/utils/tooltip'

defineOptions({ inheritAttrs: false })

/**
 * An element with something more to say than fits beside it: an absolute time
 * behind "3m ago", why a button is off. It is the trigger itself (`as`, or a
 * link with `to`) and takes its attributes. What it says shows on hover, on
 * keyboard focus and on a tap, and is the element's description for a screen
 * reader. Text that is not a control becomes a stop of the Tab order for it;
 * without `text` the element is rendered as it would be without all this.
 * The bubble itself is kept from a screen reader: it stands next to the
 * trigger, where it would be read a second time as text, and inside a label
 * it would join the field's name.
 *
 * A control that cannot be rendered from here — a field with `v-model` — goes
 * into the `trigger` slot and binds what the slot hands it.
 */
const props = withDefaults(defineProps<{
  text?: string | null
  /** The trigger's tag. */
  as?: string
  /** Renders the trigger as a link. */
  to?: RouteLocationRaw
  /**
   * The text says again what the element shows: in full where it is cut
   * short, or as the API spells it. It is then no description and no stop of
   * the Tab order: a screen reader has read it already, and a stop in every
   * row would cost the keyboard more than it gives. A pointer and a tap show
   * it, and the focus does where the element takes it anyway.
   */
  repeats?: boolean
  /**
   * One tooltip for the descendants that carry `data-tooltip`, for a list too
   * long to give each row a component of its own. As with `repeats`, it is
   * for the pointer and a tap.
   */
  within?: boolean
}>(), { text: undefined, as: 'span', to: undefined, repeats: false, within: false })

defineSlots<{
  default?: () => unknown
  trigger?: (bindings: Record<string, unknown>) => unknown
}>()

const attrs = useAttrs()
const id = useId()
const bubble = ref<HTMLElement | null>(null)
const tip = useTooltip(bubble)

const text = computed(() => props.within ? tip.said.value : props.text)
const shown = computed(() => props.within || hasText(props.text))
const describes = computed(() => shown.value && !props.repeats && !props.within)

const described = computed(() => {
  if (!describes.value) return attrs
  const others = typeof attrs['aria-describedby'] === 'string' ? `${attrs['aria-describedby']} ` : ''
  return {
    ...(!props.to && needsTabStop(props.as, props.text) ? { tabindex: 0 } : {}),
    ...attrs,
    'aria-describedby': `${others}${id}`,
  }
})

const listeners = computed(() => !shown.value ? {} : props.within ? tip.onWithin : tip.on)

/** For a trigger in the `trigger` slot: the same description and listeners, as attributes to bind. */
const bindings = computed(() => !shown.value
  ? {}
  : {
      'aria-describedby': id,
      'onPointerenter': tip.on.pointerenter,
      'onPointerleave': tip.on.pointerleave,
      'onPointerdown': tip.on.pointerdown,
      'onClick': tip.on.click,
      'onFocus': tip.on.focus,
      'onBlur': tip.on.blur,
    })

watch(() => props.text, () => {
  if (tip.open.value) void nextTick(tip.place)
})
</script>

<template>
  <slot v-if="$slots.trigger" name="trigger" v-bind="bindings" />
  <NuxtLink v-else-if="props.to" :to="props.to" v-bind="described" v-on="listeners">
    <slot />
  </NuxtLink>
  <component :is="props.as" v-else v-bind="described" v-on="listeners">
    <slot />
  </component>
  <span
    v-if="shown"
    :id="id"
    ref="bubble"
    role="tooltip"
    popover="manual"
    class="tooltip"
    :class="tip.open.value ? 'block' : 'hidden'"
    aria-hidden="true"
    v-on="tip.onBubble"
  >{{ text }}</span>
</template>
