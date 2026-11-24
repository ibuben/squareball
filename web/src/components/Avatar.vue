<script setup lang="ts">
import type { Address } from '../api'
import { avatarHue, initials } from '../format'

defineProps<{ addr?: Address; size?: number }>()
</script>

<template>
  <span class="avatar" aria-hidden="true"
        :style="{ background: `hsl(${avatarHue(addr)} 55% 48%)`, width: `${size ?? 34}px`, height: `${size ?? 34}px`,
                  fontSize: `${Math.round((size ?? 34) * 0.36)}px` }">
    {{ initials(addr) }}
  </span>
</template>
