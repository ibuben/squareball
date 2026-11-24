<script setup lang="ts">
import { ref } from 'vue'
import { Filter, FolderTree, Forward, KeyRound, Palmtree, Palette, UserRound } from 'lucide-vue-next'
import { applyTheme, state } from '../store'
import IdentitiesSettings from './settings/IdentitiesSettings.vue'
import FiltersSettings from './settings/FiltersSettings.vue'
import VacationSettings from './settings/VacationSettings.vue'
import PasswordSettings from './settings/PasswordSettings.vue'
import FoldersSettings from './settings/FoldersSettings.vue'

type Section = 'identities' | 'folders' | 'filters' | 'vacation' | 'forward' | 'password' | 'appearance'
const sections: { id: Section; label: string; icon: unknown; sieve?: boolean; password?: boolean }[] = [
  { id: 'identities', label: 'Отправители и подписи', icon: UserRound },
  { id: 'folders', label: 'Папки', icon: FolderTree },
  { id: 'filters', label: 'Фильтры', icon: Filter, sieve: true },
  { id: 'vacation', label: 'Автоответ', icon: Palmtree, sieve: true },
  { id: 'forward', label: 'Пересылка', icon: Forward, sieve: true },
  { id: 'password', label: 'Пароль', icon: KeyRound, password: true },
  { id: 'appearance', label: 'Внешний вид', icon: Palette },
]
const current = ref<Section>((sessionStorage.getItem('wm.settings') as Section) || 'identities')
function open(s: Section) {
  current.value = s
  try { sessionStorage.setItem('wm.settings', s) } catch { /* ignore */ }
}
function setTheme(th: 'light' | 'dark' | 'system') { state.theme = th; applyTheme() }
</script>

<template>
  <div class="settings">
    <nav class="nav" aria-label="Настройки">
      <h1>Настройки</h1>
      <button v-for="s in sections" :key="s.id" class="item" :class="{ on: current === s.id }"
              v-show="(!s.sieve || state.me?.features.sieve) && (!s.password || state.me?.features.password_change)" @click="open(s.id)">
        <component :is="s.icon" :size="16" /> {{ s.label }}
      </button>
    </nav>
    <div class="content scroll">
      <IdentitiesSettings v-if="current === 'identities'" />
      <FoldersSettings v-else-if="current === 'folders'" />
      <FiltersSettings v-else-if="current === 'filters'" />
      <VacationSettings v-else-if="current === 'vacation'" kind="vacation" />
      <VacationSettings v-else-if="current === 'forward'" kind="forward" />
      <PasswordSettings v-else-if="current === 'password'" />
      <section v-else>
        <h2>Внешний вид</h2>
        <div class="card">
          <label class="check"><input type="radio" value="light" :checked="state.theme === 'light'" @change="setTheme('light')" /> Светлая тема</label>
          <label class="check"><input type="radio" value="dark" :checked="state.theme === 'dark'" @change="setTheme('dark')" /> Тёмная тема</label>
          <label class="check"><input type="radio" value="system" :checked="state.theme === 'system'" @change="setTheme('system')" /> Как в системе</label>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.settings { display: grid; grid-template-columns: 240px minmax(0, 1fr); min-height: 0; height: 100%; background: var(--pane-alt); }
.nav { padding: 16px 10px; border-right: 1px solid var(--border); display: flex; flex-direction: column; gap: 2px; }
.nav h1 { font-size: 17px; margin: 0 8px 10px; }
.item { display: flex; align-items: center; gap: 10px; height: 34px; padding: 0 10px; border: 0; border-radius: 6px; background: none; text-align: left; color: var(--text); }
.item:hover { background: var(--hover); }
.item.on { background: var(--selected); font-weight: 600; }
.content { padding: 20px 28px 40px; }
.content :deep(section) { max-width: 760px; }
.content :deep(h2) { margin: 0 0 6px; font-size: 18px; }
.content :deep(.card) { display: grid; gap: 12px; margin-top: 16px; padding: 18px; border: 1px solid var(--border); border-radius: 10px; background: var(--pane); }
.content :deep(label) { display: grid; gap: 6px; color: var(--text-2); }
.content :deep(label.check) { display: flex; align-items: center; gap: 8px; color: var(--text); }
.content :deep(.actions) { display: flex; gap: 8px; flex-wrap: wrap; }
.content :deep(.error) { color: var(--danger); margin: 0; white-space: pre-wrap; }
@media (max-width: 760px) {
  .settings { grid-template-columns: 1fr; grid-template-rows: auto 1fr; }
  .nav { flex-direction: row; overflow-x: auto; border-right: 0; border-bottom: 1px solid var(--border); padding: 8px; }
  .nav h1 { display: none; }
  .item { flex: none; }
  .content { padding: 16px; }
}
</style>
