<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Trash2 } from 'lucide-vue-next'
import { api } from '../../api'
import { reportError, savePrefs, state } from '../../store'
import { dictionaryName, setPersonal, spellDictionaries, spellEngine } from '../../spell'

const engine = spellEngine()
const words = ref<string[] | null>(null)
const filter = ref('')
onMounted(async () => {
  if (engine !== 'hunspell') return
  try { words.value = (await api.spellWords()).words } catch (e) { reportError(e) }
})
async function remove(w: string) {
  try {
    words.value = (await api.deleteSpellWord(w)).words
    setPersonal(words.value)
  } catch (e) { reportError(e) }
}
const shown = () => (words.value ?? []).filter((w) => w.toLowerCase().includes(filter.value.trim().toLowerCase()))
</script>

<template>
  <section>
    <h2>Орфография</h2>
    <div class="card">
      <p class="faint" style="margin:0">
        {{ engine === 'hunspell'
          ? 'Ошибки подчёркиваются в тексте письма; правый клик по слову — варианты исправления.'
          : 'Ошибки подчёркивает браузер; языки проверки задаются в настройках браузера.' }}
      </p>
      <template v-if="engine === 'hunspell'">
        <label v-if="spellDictionaries().length > 1" class="check">
          <input type="radio" name="spell" :checked="state.prefs.spell === ''" data-test="spell-pref"
                 @change="savePrefs({ spell: '' })" /> Все словари сразу ({{ spellDictionaries().map(dictionaryName).join(', ') }})
        </label>
        <label v-for="d in spellDictionaries()" :key="d" class="check">
          <input type="radio" name="spell" :checked="state.prefs.spell === d || (spellDictionaries().length === 1 && state.prefs.spell === '')"
                 @change="savePrefs({ spell: d })" /> {{ dictionaryName(d) }}
        </label>
      </template>
      <label v-else class="check">
        <input type="radio" name="spell" :checked="state.prefs.spell !== 'off'" @change="savePrefs({ spell: '' })" /> Проверять
      </label>
      <label class="check">
        <input type="radio" name="spell" :checked="state.prefs.spell === 'off'" @change="savePrefs({ spell: 'off' })" /> Не проверять
      </label>
    </div>

    <template v-if="engine === 'hunspell'">
      <h3>Личный словарь</h3>
      <p class="faint">Слова, добавленные через «Добавить в словарь», не считаются ошибками.</p>
      <div class="card list">
        <input v-if="(words?.length ?? 0) > 10" v-model="filter" class="input" placeholder="Найти слово" />
        <div v-if="words && !words.length" class="faint empty">Словарь пуст.</div>
        <div v-for="w in shown()" :key="w" class="row" data-test="spell-word">
          <span class="ellipsis">{{ w }}</span>
          <button class="icon" title="Удалить из словаря" aria-label="Удалить из словаря" @click="remove(w)"><Trash2 :size="15" /></button>
        </div>
      </div>
    </template>
  </section>
</template>

<style scoped>
h3 { font-size: 15px; margin: 22px 0 4px; }
.card.list { gap: 0 !important; padding: 6px 14px !important; }
.card.list .input { margin: 6px 0; }
.row { display: flex; align-items: center; justify-content: space-between; gap: 10px; min-height: 38px; border-bottom: 1px solid var(--border); }
.row:last-child { border-bottom: 0; }
.empty { padding: 8px 0; }
.icon { display: inline-flex; border: 0; background: none; padding: 6px; border-radius: 6px; color: var(--text-2); }
.icon:hover { background: var(--hover); color: var(--danger); }
</style>
