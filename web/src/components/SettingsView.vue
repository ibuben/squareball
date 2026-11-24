<script setup lang="ts">
import { ref } from 'vue'
import {
  BookOpen, FileText, Filter, FolderTree, Forward, KeyRound, Palmtree, Palette, SpellCheck, UserRound,
} from 'lucide-vue-next'
import { loadPrefs, savePrefs, setTheme, state } from '../store'
import { availableLangs, lang, langName, setLang, t, type Key } from '../i18n'
import IdentitiesSettings from './settings/IdentitiesSettings.vue'
import FiltersSettings from './settings/FiltersSettings.vue'
import VacationSettings from './settings/VacationSettings.vue'
import PasswordSettings from './settings/PasswordSettings.vue'
import FoldersSettings from './settings/FoldersSettings.vue'
import TemplatesSettings from './settings/TemplatesSettings.vue'
import SpellSettings from './settings/SpellSettings.vue'

type Section = 'identities' | 'templates' | 'spell' | 'reading' | 'folders' | 'filters' | 'vacation' | 'forward' | 'password' | 'appearance'
const sections: { id: Section; label: Key; icon: unknown; sieve?: boolean; password?: boolean; spell?: boolean }[] = [
  { id: 'identities', label: 'set.identities', icon: UserRound },
  { id: 'templates', label: 'set.templates', icon: FileText },
  { id: 'spell', label: 'set.spell', icon: SpellCheck, spell: true },
  { id: 'reading', label: 'set.reading', icon: BookOpen },
  { id: 'folders', label: 'set.folders', icon: FolderTree },
  { id: 'filters', label: 'set.filters', icon: Filter, sieve: true },
  { id: 'vacation', label: 'set.vacation', icon: Palmtree, sieve: true },
  { id: 'forward', label: 'set.forward', icon: Forward, sieve: true },
  { id: 'password', label: 'set.password', icon: KeyRound, password: true },
  { id: 'appearance', label: 'set.appearance', icon: Palette },
]
const current = ref<Section>((sessionStorage.getItem('wm.settings') as Section) || 'identities')
function open(s: Section) {
  current.value = s
  try { sessionStorage.setItem('wm.settings', s) } catch { /* ignore */ }
}
const mdnModes = [
  { v: '' as const, label: 'set.mdnAsk' as Key },
  { v: 'always' as const, label: 'set.mdnAlways' as Key },
  { v: 'never' as const, label: 'set.mdnNever' as Key },
]
void loadPrefs()
const langs = () => availableLangs()
</script>

<template>
  <div class="settings">
    <nav class="nav" :aria-label="t('nav.settings')">
      <h1>{{ t('nav.settings') }}</h1>
      <button v-for="s in sections" :key="s.id" class="item" :class="{ on: current === s.id }"
              v-show="(!s.sieve || state.me?.features.sieve) && (!s.password || state.me?.features.password_change)
                && (!s.spell || state.me?.features.spellcheck?.engine !== 'off')" @click="open(s.id)">
        <component :is="s.icon" :size="16" /> {{ t(s.label) }}
      </button>
    </nav>
    <div class="content scroll">
      <IdentitiesSettings v-if="current === 'identities'" />
      <TemplatesSettings v-else-if="current === 'templates'" />
      <SpellSettings v-else-if="current === 'spell'" />
      <section v-else-if="current === 'reading'">
        <h2>{{ t('set.reading') }}</h2>
        <div class="card">
          <p class="faint" style="margin:0">{{ t('set.mdnHint') }}</p>
          <label v-for="o in mdnModes" :key="o.v" class="check">
            <input type="radio" name="mdn" :value="o.v" :checked="state.prefs.mdn_mode === o.v" data-test="mdn-mode"
                   @change="savePrefs({ mdn_mode: o.v })" /> {{ t(o.label) }}
          </label>
        </div>
      </section>
      <FoldersSettings v-else-if="current === 'folders'" />
      <FiltersSettings v-else-if="current === 'filters'" />
      <VacationSettings v-else-if="current === 'vacation'" kind="vacation" />
      <VacationSettings v-else-if="current === 'forward'" kind="forward" />
      <PasswordSettings v-else-if="current === 'password'" />
      <section v-else>
        <h2>{{ t('set.appearance') }}</h2>
        <div class="card">
          <label class="check"><input type="radio" value="light" :checked="state.theme === 'light'" @change="setTheme('light')" /> {{ t('set.themeLight') }}</label>
          <label class="check"><input type="radio" value="dark" :checked="state.theme === 'dark'" @change="setTheme('dark')" /> {{ t('set.themeDark') }}</label>
          <label class="check"><input type="radio" value="system" :checked="state.theme === 'system'" @change="setTheme('system')" /> {{ t('theme.system') }}</label>
          <label v-if="langs().length > 1" class="lang">{{ t('login.language') }}
            <select class="input" :value="lang()" data-test="settings-lang" @change="setLang(($event.target as HTMLSelectElement).value)">
              <option v-for="l in langs()" :key="l" :value="l">{{ langName(l) }}</option>
            </select>
          </label>
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
.lang { max-width: 260px; margin-top: 6px; }
.content :deep(.error) { color: var(--danger); margin: 0; white-space: pre-wrap; }
@media (max-width: 760px) {
  .settings { grid-template-columns: 1fr; grid-template-rows: auto 1fr; }
  .nav { flex-direction: row; overflow-x: auto; border-right: 0; border-bottom: 1px solid var(--border); padding: 8px; }
  .nav h1 { display: none; }
  .item { flex: none; }
  .content { padding: 16px; }
}
</style>
