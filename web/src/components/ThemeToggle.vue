<script setup lang="ts">
// Переключатель темы по кругу: светлая → тёмная → как в системе. Иконка — текущий режим.
import { computed } from 'vue'
import { Moon, Sun, SunMoon } from 'lucide-vue-next'
import { t } from '../i18n'
import { nextTheme, setTheme, state } from '../store'

defineProps<{ size?: number }>()

const icon = computed(() => ({ light: Sun, dark: Moon, system: SunMoon })[state.theme])
const label = computed(() => t('theme.toggle', { mode: t(`theme.${state.theme}`) }))
</script>

<template>
  <button type="button" class="icon-btn" :title="label" :aria-label="label" data-test="theme-toggle"
          :data-theme-mode="state.theme" @click="setTheme(nextTheme(state.theme))">
    <component :is="icon" :size="size ?? 20" />
  </button>
</template>
