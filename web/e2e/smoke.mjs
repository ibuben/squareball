// Сквозной smoke-тест против живого стенда (deploy/devstand).
// Запуск: BASE=http://127.0.0.1:8080 node e2e/smoke.mjs
// Переменные: CHROMIUM (путь к браузеру), SHOTS (каталог для скриншотов),
// USER1/USER2/PASS — учётки стенда, PANEL_KEY — ключ SSO адаптера panel.
import { chromium, request } from 'playwright-core'
import { existsSync, mkdirSync, readFileSync } from 'node:fs'
import { createHmac, randomUUID } from 'node:crypto'

const BASE = process.env.BASE ?? 'http://127.0.0.1:8080'
const SHOTS = process.env.SHOTS ?? 'e2e/out'
const USER1 = process.env.USER1 ?? 'bob'
const USER2 = process.env.USER2 ?? 'alice@test.local'
const PASS = process.env.PASS ?? 'Test12345!'
mkdirSync(SHOTS, { recursive: true })

const browser = await chromium.launch({ executablePath: process.env.CHROMIUM ?? '/usr/bin/chromium', args: ['--no-sandbox'] })
const errors = []
const ctx = await browser.newContext({ viewport: { width: 1440, height: 860 }, locale: 'ru-RU' })
// Основной сценарий проверяет русские надписи; язык по умолчанию — английский (см. checkLanguage).
await ctx.addInitScript(() => { if (!localStorage.getItem('wm.lang')) localStorage.setItem('wm.lang', 'ru') })
const page = await ctx.newPage()
page.on('pageerror', (e) => errors.push(e.message))
// 401 до входа и при неверном пароле — ожидаемые ответы API, не ошибки.
let expectHttpError = false // шаг намеренно получает ошибку от API
page.on('console', (m) => { if (m.type() === 'error' && !m.text().includes('status of 401') && !expectHttpError) errors.push(m.text()) })
const step = (name) => console.log('✓', name)
const shot = (name) => page.screenshot({ path: `${SHOTS}/${name}.png` })

// Нажимает кнопку и ждёт ответа PUT /api/v1/sieve; ошибка сохранения — падение теста.
async function saveSieveVia(button) {
  const [res] = await Promise.all([
    page.waitForResponse((r) => r.url().endsWith('/api/v1/sieve') && r.request().method() === 'PUT'),
    button.click(),
  ])
  if (!res.ok()) throw new Error(`сохранение Sieve: ${res.status()} ${await res.text()}`)
}

// USER2 отправляет USER1 письма через API клиента (данные для теста и проверка push).
async function sendAs(subjects, withAttachment = false) {
  const api = await request.newContext({ baseURL: BASE, extraHTTPHeaders: { Origin: BASE } })
  const login = await api.post('/api/v1/auth/login', { data: { username: USER2, password: PASS } })
  if (!login.ok()) throw new Error(`seed login: ${login.status()}`)
  const { csrf } = await login.json()
  const to = USER1.includes('@') ? USER1 : `${USER1}@test.local`
  for (const [i, subject] of subjects.entries()) {
    const draft = {
      to: [{ email: to }], subject,
      html: `<p>Привет! Это письмо <b>«${subject}»</b>.</p><img src="https://example.com/pixel.png"><script>alert(1)</script>`,
    }
    const r = await api.post('/api/v1/send', {
      headers: { 'X-CSRF-Token': csrf },
      multipart: withAttachment && i === 0
        ? { draft: JSON.stringify(draft), attachments: { name: 'заметка.txt', mimeType: 'text/plain', buffer: Buffer.from('hello') } }
        : { draft: JSON.stringify(draft) },
    })
    if (!r.ok()) throw new Error(`seed send: ${r.status()} ${await r.text()}`)
  }
  await api.post('/api/v1/auth/logout', { headers: { 'X-CSRF-Token': csrf } })
  await api.dispose()
}
const seed = () => sendAs(['Письмо для e2e №1', 'Письмо для e2e №2', 'Письмо для e2e №3'], true)

// USER2 приглашает USER1 на встречу: письмо с вложением invite.ics (METHOD:REQUEST).
async function sendInvite(summary) {
  const api = await request.newContext({ baseURL: BASE, extraHTTPHeaders: { Origin: BASE } })
  const login = await api.post('/api/v1/auth/login', { data: { username: USER2, password: PASS } })
  if (!login.ok()) throw new Error(`invite login: ${login.status()}`)
  const { csrf } = await login.json()
  const to = USER1.includes('@') ? USER1 : `${USER1}@test.local`
  const from = USER2.includes('@') ? USER2 : `${USER2}@test.local`
  const ics = ['BEGIN:VCALENDAR', 'VERSION:2.0', 'PRODID:-//e2e//RU', 'METHOD:REQUEST', 'BEGIN:VEVENT',
    `UID:e2e-${Date.now()}@test.local`, 'SEQUENCE:0', 'DTSTAMP:20261124T080000Z',
    'DTSTART;TZID=Asia/Tashkent:20261201T100000', 'DTEND;TZID=Asia/Tashkent:20261201T110000',
    `SUMMARY:${summary}`, 'LOCATION:Переговорная 3', 'URL:https://meet.example.com/e2e',
    `ORGANIZER;CN=Алиса:mailto:${from}`,
    `ATTENDEE;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:${to}`,
    'ATTENDEE;ROLE=REQ-PARTICIPANT;PARTSTAT=ACCEPTED;CN=Кэрол:mailto:carol@test.local',
    'ATTENDEE;ROLE=OPT-PARTICIPANT;PARTSTAT=DECLINED;CN=Дэйв:mailto:dave@test.local',
    'END:VEVENT', 'END:VCALENDAR', ''].join('\r\n')
  const r = await api.post('/api/v1/send', {
    headers: { 'X-CSRF-Token': csrf },
    multipart: {
      draft: JSON.stringify({ to: [{ email: to }], subject: `Приглашение: ${summary}`, html: '<p>Обсудим итоги недели.</p>' }),
      attachments: { name: 'invite.ics', mimeType: 'text/calendar', buffer: Buffer.from(ics) },
    },
  })
  if (!r.ok()) throw new Error(`invite send: ${r.status()} ${await r.text()}`)
  return { api, csrf }
}

// Экран входа: английский по умолчанию, выбор языка запоминается, переключатель темы по кругу.
async function checkLanguage() {
  const c = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  const p = await c.newPage()
  await p.goto(BASE)
  await p.getByRole('button', { name: 'Sign in' }).waitFor()
  await p.locator('[data-test=login-lang]').selectOption('ru')
  await p.getByRole('button', { name: 'Войти' }).waitFor()
  await p.reload()
  await p.getByRole('button', { name: 'Войти' }).waitFor()
  if (await p.evaluate(() => document.documentElement.lang) !== 'ru') throw new Error('lang не ru после выбора')
  await p.locator('[data-test=login-lang]').selectOption('en')
  await p.getByRole('button', { name: 'Sign in' }).waitFor()
  const toggle = p.locator('[data-test=theme-toggle]')
  const seen = []
  for (let i = 0; i < 3; i++) {
    await toggle.click()
    seen.push(`${await toggle.getAttribute('data-theme-mode')}/${await p.evaluate(() => document.documentElement.dataset.theme)}`)
  }
  if (!seen[0].startsWith('light/light') || !seen[1].startsWith('dark/dark') || !seen[2].startsWith('system/')) throw new Error(`переключатель темы: ${seen}`)
  await p.screenshot({ path: `${SHOTS}/00-login-en.png` })
  // После входа: тема — на левой панели, «О программе» — под настройками.
  await p.locator('input[autocomplete=username]').fill(USER1)
  await p.locator('input[type=password]').fill(PASS)
  await p.getByRole('button', { name: 'Sign in' }).click()
  await p.locator('.rail [data-test=theme-toggle]').waitFor()
  if (await p.locator('.menu').getByText('Theme').count()) throw new Error('тема осталась в меню')
  await p.locator('.rail [data-test=about]').click()
  await p.locator('[data-test=about-dialog]').getByText('eXUnity LAB').waitFor()
  await p.screenshot({ path: `${SHOTS}/00-about.png` })
  await p.keyboard.press('Escape')
  await p.locator('[data-test=about-dialog]').waitFor({ state: 'detached' })
  await c.close()
}

try {
  await seed()
  step('тестовые письма отправлены через API')
  await checkLanguage()
  step('английский по умолчанию, выбор языка и темы на входе, «О программе»')
  await page.goto(BASE)
  await page.getByRole('button', { name: 'Войти' }).waitFor()
  await shot('01-login')

  await page.locator('input[autocomplete=username]').fill(USER1)
  await page.locator('input[type=password]').fill('wrong-password')
  await page.getByRole('button', { name: 'Войти' }).click()
  await page.getByRole('alert').waitFor()
  step('неверный пароль показывает ошибку')

  await page.locator('input[type=password]').fill(PASS)
  await page.getByRole('button', { name: 'Войти' }).click()
  await page.locator('.item').first().waitFor()
  step(`вход, писем во входящих: ${await page.locator('.item').count()}`)
  await shot('02-inbox')

  await page.locator('.item').first().click()
  const frame = page.frameLocator('iframe.body')
  await frame.locator('body').waitFor()
  const bodyText = await frame.locator('body').innerText()
  step(`письмо открыто: «${bodyText.trim().slice(0, 40)}»`)
  if (!(await page.getByText('Картинки из интернета скрыты').count())) throw new Error('внешняя картинка не заблокирована')
  const scripts = await frame.locator('script').count()
  if (scripts) throw new Error('в письме остался <script>')
  await shot('03-reading')

  await page.keyboard.press('r')
  await page.getByRole('dialog').waitFor()
  // Курсор сразу в редакторе; цитата исходного письма — отдельно, под ним.
  await page.locator('.ProseMirror:focus').waitFor()
  await page.keyboard.type('Проверка ответа из e2e. ')
  await page.keyboard.press('Control+b')
  await page.keyboard.type('Жирный текст.')
  if (await page.getByRole('dialog').locator('.ProseMirror blockquote').count()) throw new Error('цитата попала в редактор')
  if (!(await page.getByRole('dialog').locator('.ProseMirror strong', { hasText: 'Жирный текст' }).count())) throw new Error('Ctrl+B не сработал')
  await page.getByRole('dialog').getByRole('button', { name: 'Показать цитату' }).click()
  await page.frameLocator('iframe.quote-frame').getByText('Привет!').first().waitFor()
  await shot('04-reply')
  await page.getByRole('dialog').getByRole('button', { name: 'Отправить' }).click()
  await page.getByText('Письмо отправлено').waitFor()
  step('ответ отправлен')

  await page.locator('.folders').getByRole('button', { name: 'Новое письмо' }).click()
  const compose = page.getByRole('dialog').last()
  await compose.locator('.chips input').first().fill(USER2)
  await compose.locator('.field input').last().fill('Письмо из e2e-теста')
  await compose.locator('.ProseMirror').click()
  await page.keyboard.type('Привет! Это проверка отправки.')
  await compose.locator('input[type=file]:not([data-test=inline-image])').setInputFiles({ name: 'отчёт.txt', mimeType: 'text/plain', buffer: Buffer.from('данные') })
  await compose.getByRole('button', { name: 'Отправить' }).click()
  await page.getByText('Письмо отправлено').last().waitFor()
  step('новое письмо с вложением отправлено')

  await page.locator('.folder', { hasText: 'Отправленные' }).click()
  await page.locator('.item', { hasText: 'Письмо из e2e-теста' }).first().waitFor()
  step('копия в «Отправленных»')
  await page.locator('.item', { hasText: 'Re: Письмо для e2e' }).first().click()
  const sentBody = await page.frameLocator('iframe.body').locator('body').innerText()
  if (!sentBody.includes('Проверка ответа') || !sentBody.includes('Привет! Это письмо')) {
    throw new Error('в отправленном ответе нет текста или цитаты: ' + sentBody.slice(0, 200))
  }
  step('ответ ушёл с текстом и цитатой исходного письма')

  // Черновик: автосохранение, закрытие окна, продолжение из папки, отправка.
  const draftSubject = `Черновик e2e ${Date.now()}`
  await page.locator('.folders').getByRole('button', { name: 'Новое письмо' }).click()
  const dlg = page.getByRole('dialog').last()
  await dlg.locator('.field input').last().fill(draftSubject)
  await dlg.locator('.ProseMirror').click()
  await page.keyboard.type('Текст черновика')
  await dlg.locator('[data-draft-status]', { hasText: 'Сохранено в' }).waitFor({ timeout: 15000 })
  step('черновик сохранился автоматически')
  await dlg.locator('.chips input').first().fill(USER2)
  await dlg.locator('.chips input').first().press('Enter')
  await dlg.getByRole('button', { name: 'close' }).click()
  await page.getByText('Черновик сохранён').waitFor()
  await page.locator('.folder', { hasText: 'Черновики' }).click()
  await page.locator('.item', { hasText: draftSubject }).click()
  await page.getByRole('button', { name: 'Продолжить' }).click()
  const dlg2 = page.getByRole('dialog').last()
  if ((await dlg2.locator('.field input').last().inputValue()) !== draftSubject) throw new Error('тема черновика не восстановилась')
  if (!(await dlg2.locator('.chip').count())) throw new Error('получатель черновика не восстановился')
  await dlg2.getByRole('button', { name: 'Отправить' }).click()
  await dlg2.waitFor({ state: 'detached' })
  await page.locator('.item', { hasText: draftSubject }).waitFor({ state: 'detached' })
  step('черновик продолжен, отправлен и удалён из «Черновиков»')

  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/messages?folder=INBOX')),
    page.locator('.folder', { hasText: 'Входящие' }).click(),
  ])
  await page.locator('.item').first().waitFor()

  // Push: письмо от другого пользователя появляется без перезагрузки.
  // Опрос при живом потоке идёт раз в 2 минуты, так что успеть за 10 с может только push.
  const pushSubject = `Push e2e ${Date.now()}`
  await sendAs([pushSubject])
  await page.locator('.item', { hasText: pushSubject }).waitFor({ timeout: 10000 })
  step('новое письмо пришло через IDLE без перезагрузки')

  // Настройки: подпись отправителя попадает в письмо.
  const sigText = `С уважением, Боб ${Date.now()}`
  await page.getByRole('button', { name: 'Настройки' }).first().click()
  await page.getByRole('heading', { name: 'Отправители и подписи' }).waitFor()
  await page.locator('.row').first().click()
  await page.getByLabel('Имя').fill('Боб Тестов')
  await page.locator('.sig-editor .ProseMirror').click()
  await page.keyboard.press('Control+a')
  await page.keyboard.type(sigText)
  await page.getByRole('button', { name: 'Сохранить' }).click()
  await page.getByText('Сохранено: Боб Тестов').waitFor()
  await shot('08-settings-identity')
  await page.getByRole('button', { name: 'Почта' }).first().click()
  await page.locator('.folders').getByRole('button', { name: 'Новое письмо' }).click()
  const sigDlg = page.getByRole('dialog').last()
  await sigDlg.locator('.sig', { hasText: sigText }).waitFor()
  const sigSubject = `Подпись e2e ${Date.now()}`
  await sigDlg.locator('.chips input').first().fill(USER2)
  await sigDlg.locator('.chips input').first().press('Enter')
  await sigDlg.locator('.field input').last().fill(sigSubject)
  await sigDlg.locator('.ProseMirror').click()
  await page.keyboard.type('Письмо с подписью.')
  await sigDlg.getByRole('button', { name: 'Отправить' }).click()
  await sigDlg.waitFor({ state: 'detached' }) // окно закрывается только после успешной отправки
  await page.locator('.folder', { hasText: 'Отправленные' }).click()
  await page.locator('.item', { hasText: sigSubject }).click()
  const sentText = await page.frameLocator('iframe.body').locator('body').innerText()
  if (!sentText.includes(sigText) || !sentText.includes('Письмо с подписью')) throw new Error('подписи нет в письме: ' + sentText)
  if (!(await page.locator('.who', { hasText: 'Боб Тестов' }).count())) throw new Error('имя отправителя не применилось')
  step('подпись и имя отправителя попали в письмо')

  // Фильтр: письмо с меткой в теме сервер кладёт в «Архив».
  const tagSubject = `[e2e-filter-${Date.now()}]`
  await page.getByRole('button', { name: 'Настройки' }).first().click()
  await page.locator('.item', { hasText: 'Фильтры' }).click()
  await page.getByRole('button', { name: 'Добавить правило' }).waitFor()
  // Остатки прошлых прогонов (если тест падал до уборки).
  while (await page.locator('.row', { hasText: 'e2e:' }).count()) {
    page.once('dialog', (d) => d.accept())
    await saveSieveVia(page.locator('.row', { hasText: 'e2e:' }).first().getByRole('button', { name: 'Удалить' }))
  }
  const ruleName = `e2e: в архив ${Date.now()}`
  await page.getByRole('button', { name: 'Добавить правило' }).click()
  const form = page.locator('form.card')
  await form.getByLabel('Название').fill(ruleName)
  await form.locator('.line').nth(1).locator('select').first().selectOption('subject')
  await form.locator('.line').nth(1).locator('input.val').fill(tagSubject)
  await saveSieveVia(form.getByRole('button', { name: 'Сохранить правило' }))
  await page.locator('.row', { hasText: ruleName }).waitFor()
  await shot('09-settings-filters')
  await sendAs([`${tagSubject} письмо для фильтра`])
  await page.getByRole('button', { name: 'Почта' }).first().click()
  await page.locator('.folder', { hasText: 'Архив' }).click()
  await page.locator('.item', { hasText: tagSubject }).waitFor({ timeout: 10000 })
  step('фильтр Sieve переложил письмо в «Архив»')

  // Автоответ сохраняется в тот же скрипт; затем убираем за собой правило и автоответ.
  await page.getByRole('button', { name: 'Настройки' }).first().click()
  await page.locator('.item', { hasText: 'Автоответ' }).click()
  await page.locator('[data-test=vacation-enabled]').check()
  await page.getByLabel('Текст ответа').fill('Я в отпуске (e2e).')
  await saveSieveVia(page.getByRole('button', { name: 'Сохранить' }))
  await page.locator('.item', { hasText: 'Фильтры' }).click()
  await page.getByRole('button', { name: 'Редактировать как текст' }).click()
  const script = await page.locator('textarea.raw').inputValue()
  if (!script.includes('vacation') || !script.includes(`# rule:[${ruleName}]`)) throw new Error('скрипт без правила или автоответа:\n' + script)
  await page.getByRole('button', { name: 'Вернуться к правилам' }).click()
  page.once('dialog', (d) => d.accept())
  await saveSieveVia(page.locator('.row', { hasText: ruleName }).getByRole('button', { name: 'Удалить' }))
  await page.locator('.row', { hasText: ruleName }).waitFor({ state: 'detached' })
  await page.locator('.item', { hasText: 'Автоответ' }).click()
  await page.locator('[data-test=vacation-enabled]').uncheck()
  await saveSieveVia(page.getByRole('button', { name: 'Сохранить' }))
  step('автоответ сохранён в Sieve; правило и автоответ убраны')

  // Смена пароля (если на сервере настроен драйвер): туда и обратно.
  if (await page.locator('.nav .item', { hasText: 'Пароль' }).isVisible()) {
    const NEWPASS = 'E2e-Новый-Пароль-7'
    const change = async (from, to) => {
      await page.locator('.nav .item', { hasText: 'Пароль' }).click()
      const cur = page.locator('[data-test=pw-current]')
      await cur.fill(from)
      await page.locator('[data-test=pw-new]').fill('short')
      await page.locator('[data-test=pw-repeat]').fill('short')
      if (await page.getByRole('button', { name: 'Сменить пароль' }).isEnabled()) throw new Error('слабый пароль принят формой')
      await page.locator('[data-test=pw-new]').fill(to)
      await page.locator('[data-test=pw-repeat]').fill(to)
      const res = page.waitForResponse((r) => r.url().endsWith('/api/v1/account/password') && r.request().method() === 'POST')
      await page.getByRole('button', { name: 'Сменить пароль' }).click()
      if (!(await res).ok()) throw new Error(`смена пароля: HTTP ${(await res).status()}`)
      await page.getByText('Пароль изменён').first().waitFor()
      if (await cur.inputValue()) throw new Error('форма не очистилась после смены')
    }
    await change(PASS, NEWPASS)
    try {
      const other = await browser.newContext()
      const ok = await other.request.post(`${BASE}/api/v1/auth/login`, { data: { username: USER1, password: NEWPASS }, headers: { Origin: BASE } })
      await other.close()
      if (!ok.ok()) throw new Error(`вход с новым паролем: HTTP ${ok.status()}`)
      // Сессия, в которой сменили пароль, продолжает работать с IMAP.
      await page.getByRole('button', { name: 'Почта' }).first().click()
      await page.locator('.item').first().waitFor()
      await page.getByRole('button', { name: 'Настройки' }).first().click()
    } finally {
      await change(NEWPASS, PASS)
    }
    step('пароль сменён, вход с новым паролем работает, пароль возвращён')
  }
  await page.getByRole('button', { name: 'Почта' }).first().click()

  // Контакты: группа, контакт в ней, импорт vCard, подсказка группы в «Кому».
  const stamp = Date.now()
  const groupName = `e2e группа ${stamp}`
  await page.getByRole('button', { name: 'Контакты' }).first().click()
  await page.getByRole('heading', { name: 'Контакты' }).waitFor()
  page.once('dialog', (d) => d.accept(groupName))
  await page.getByRole('button', { name: 'Новая группа' }).click()
  await page.locator('.src.on', { hasText: groupName }).waitFor()
  await page.getByRole('button', { name: 'Новый контакт' }).click()
  const cform = page.locator('form.card')
  await cform.getByLabel('Имя', { exact: true }).fill('Алиса')
  await cform.getByLabel('Фамилия').fill(`E2E ${stamp}`)
  await cform.locator('input[type=email]').first().fill(USER2)
  await cform.getByRole('button', { name: 'Сохранить' }).click()
  await page.locator('.item', { hasText: `Алиса E2E ${stamp}` }).waitFor()
  await page.locator('.src', { hasText: 'Все контакты' }).click()
  await page.locator('input[type=file][accept*=vcf]').setInputFiles({
    name: 'import.vcf', mimeType: 'text/vcard',
    buffer: Buffer.from(`BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Импорт ${stamp}\r\nEMAIL:import-${stamp}@test.local\r\nEND:VCARD\r\n`),
  })
  await page.getByText('Импортировано: 1').waitFor()
  await page.locator('.item', { hasText: `Импорт ${stamp}` }).waitFor()
  await shot('10-contacts')
  step('контакт в группе создан, vCard импортирован')

  await page.getByRole('button', { name: 'Почта' }).first().click()
  await page.locator('.folders').getByRole('button', { name: 'Новое письмо' }).click()
  const gdlg = page.getByRole('dialog').last()
  await gdlg.locator('.chips input').first().fill('e2e груп')
  await gdlg.locator('.suggest li', { hasText: groupName }).waitFor()
  await shot('11-suggest-group')
  await gdlg.locator('.chips input').first().press('Enter')
  await gdlg.locator('.chip', { hasText: 'Алиса' }).waitFor()
  step('группа из подсказки раскрылась в адреса участников')
  await gdlg.getByRole('button', { name: 'Удалить черновик' }).click() // без подтверждения
  await gdlg.waitFor({ state: 'detached' })

  // Уборка: контакты и группа этого прогона.
  await page.getByRole('button', { name: 'Контакты' }).first().click()
  for (const name of [`Алиса E2E ${stamp}`, `Импорт ${stamp}`]) {
    await page.locator('.item', { hasText: name }).click()
    page.once('dialog', (d) => d.accept())
    await page.locator('.detail').getByRole('button', { name: 'Удалить' }).click()
    await page.locator('.item', { hasText: name }).waitFor({ state: 'detached' })
  }
  await page.locator('.src', { hasText: groupName }).click()
  page.once('dialog', (d) => d.accept())
  await page.locator('.src', { hasText: groupName }).getByRole('button', { name: 'Удалить группу' }).click()
  await page.locator('.src', { hasText: groupName }).waitFor({ state: 'detached' })
  await page.getByRole('button', { name: 'Почта' }).first().click()

  // Внешняя адресная книга (CardDAV, Radicale стенда): пресет с паролем ящика и своя учётка.
  const me = await (await page.request.get(`${BASE}/api/v1/auth/me`)).json()
  if (me.features.contact_books) {
    const dav = (p, method = 'GET') => fetch(`http://127.0.0.1:5232${p}`, {
      method, headers: { Authorization: 'Basic ' + Buffer.from(`${USER1}:${PASS}`).toString('base64'), Depth: '1' } })
    await page.getByRole('button', { name: 'Контакты' }).first().click()
    const book = page.locator('.src', { hasText: 'Контакты Боба' })
    await book.waitFor({ timeout: 15000 })
    await book.click()
    await page.getByRole('button', { name: 'Новый контакт' }).click()
    const bform = page.locator('form.card')
    await bform.locator('[data-test=contact-book]').waitFor()
    if (await bform.locator('[data-test=contact-book] option:checked').textContent() === 'Личные') throw new Error('книга по умолчанию не выбрана')
    await bform.getByLabel('Имя', { exact: true }).fill('Дав')
    await bform.getByLabel('Фамилия').fill(`Карточкин ${stamp}`)
    await bform.locator('input[type=email]').first().fill(`dav-${stamp}@example.org`)
    await bform.getByRole('button', { name: 'Сохранить' }).click()
    await page.locator('.item', { hasText: `Дав Карточкин ${stamp}` }).waitFor()
    const listing = await (await dav(`/${USER1}/contacts/`, 'PROPFIND')).text()
    const href = listing.match(/<href>([^<]+\.vcf)<\/href>/g)?.map((h) => h.slice(6, -7)) ?? []
    const cards = await Promise.all(href.map(async (h) => (await dav(h)).text()))
    if (!cards.some((c) => c.includes(`dav-${stamp}@example.org`))) throw new Error('карточки нет на CardDAV-сервере')
    // Правка уходит на сервер.
    await page.locator('.detail').getByRole('button', { name: 'Изменить' }).click()
    await bform.getByLabel('Организация').fill('Радикале и партнёры')
    await bform.getByRole('button', { name: 'Сохранить' }).click()
    // Тост «Контакт сохранён» мог остаться от создания — ждём саму правку в карточке.
    await page.locator('.detail .muted', { hasText: 'Радикале и партнёры' }).waitFor()
    const after = await Promise.all(href.map(async (h) => (await dav(h)).text()))
    if (!after.some((c) => c.includes('ORG:Радикале и партнёры'))) {
      const fresh = await (await dav(`/${USER1}/contacts/`, 'PROPFIND')).text()
      const hs = fresh.match(/<href>([^<]+\.vcf)<\/href>/g)?.map((h) => h.slice(6, -7)) ?? []
      const all = await Promise.all(hs.map(async (h) => (await dav(h)).text()))
      if (!all.some((c) => c.includes('ORG:Радикале и партнёры'))) throw new Error('правка не дошла до сервера')
    }
    await shot('22-carddav-book')
    page.once('dialog', (d) => d.accept())
    await page.locator('.detail').getByRole('button', { name: 'Удалить' }).click()
    await page.locator('.item', { hasText: `Дав Карточкин ${stamp}` }).waitFor({ state: 'detached' })
    const left = await (await dav(`/${USER1}/contacts/`, 'PROPFIND')).text()
    const leftCards = await Promise.all((left.match(/<href>([^<]+\.vcf)<\/href>/g) ?? []).map(async (h) => (await dav(h.slice(6, -7))).text()))
    if (leftCards.some((c) => c.includes(`dav-${stamp}@example.org`))) throw new Error('карточка осталась на сервере')
    step('CardDAV: контакт создан, изменён и удалён во внешней книге')

    // Своя учётка: неверный пароль, затем подключение и отключение.
    await page.locator('[data-test=books-add]').click()
    const af = page.locator('[data-test=books-form]')
    await af.locator('[data-test=books-url]').fill('http://127.0.0.1:5232/')
    await af.locator('[data-test=books-user]').fill(USER1)
    await af.locator('[data-test=books-password]').fill('wrong')
    await af.getByLabel('Название').fill(`Своя ${stamp}`)
    expectHttpError = true
    await af.getByRole('button', { name: 'Подключить' }).click()
    await af.getByText('Сервер не принял логин или пароль.').waitFor()
    expectHttpError = false
    await af.locator('[data-test=books-password]').fill(PASS)
    await af.getByRole('button', { name: 'Подключить' }).click()
    await page.getByText('Адресная книга подключена').waitFor()
    const acct = page.locator('.acct', { hasText: `Своя ${stamp}` })
    await acct.waitFor()
    await shot('23-carddav-account')
    page.once('dialog', (d) => d.accept())
    await acct.hover()
    await acct.getByRole('button', { name: `Отключить Своя ${stamp}` }).click()
    await acct.waitFor({ state: 'detached' })
    step('CardDAV: своя учётка — ошибка пароля, подключение, отключение')
    await page.getByRole('button', { name: 'Почта' }).first().click()
  }

  // Папки: создать, подпапка, переименовать с подпапкой, отписка в настройках, удалить.
  const fname = `e2e папка ${stamp}`
  const pane = page.locator('nav.folders')
  page.once('dialog', (d) => d.accept(fname))
  await page.locator('[data-test=folder-new]').click()
  await pane.locator('.folder', { hasText: fname }).waitFor()
  await pane.locator('.folder', { hasText: fname }).click({ button: 'right' })
  page.once('dialog', (d) => d.accept('Вложенная'))
  await page.locator('[data-test=folder-menu]').getByRole('menuitem', { name: 'Новая подпапка' }).click()
  await pane.locator('.folder', { hasText: 'Вложенная' }).waitFor()
  await pane.locator('.folder', { hasText: fname }).click({ button: 'right' })
  await shot('12-folder-menu')
  page.once('dialog', (d) => d.accept(fname + ' new'))
  await page.locator('[data-test=folder-menu]').getByRole('menuitem', { name: 'Переименовать' }).click()
  await pane.locator(`.folder[data-folder="${fname} new/Вложенная"]`).waitFor()
  step('папка и подпапка созданы, переименование перенесло подпапку')

  await page.getByRole('button', { name: 'Настройки' }).first().click()
  await page.locator('.nav .item', { hasText: 'Папки' }).click()
  const subRow = page.locator(`.row[data-folder="${fname} new/Вложенная"]`)
  await Promise.all([
    page.waitForResponse((r) => r.url().endsWith('/api/v1/folders/subscribe')),
    subRow.locator('input[type=checkbox]').uncheck(),
  ])
  await shot('13-settings-folders')
  await page.getByRole('button', { name: 'Почта' }).first().click()
  if (await pane.locator('.folder', { hasText: 'Вложенная' }).count()) throw new Error('отписанная папка видна в списке')
  await pane.locator('.folder', { hasText: fname + ' new' }).click({ button: 'right' })
  page.once('dialog', (d) => d.accept())
  await page.locator('[data-test=folder-menu]').getByRole('menuitem', { name: 'Удалить папку' }).click()
  await pane.locator('.folder', { hasText: fname }).waitFor({ state: 'detached' })
  step('отписанная папка скрыта; папка с подпапкой удалена')

  // Архив по месяцам: «В архив» создаёт Архив/ГГГГ/ММ.
  await page.getByRole('button', { name: 'Настройки' }).first().click()
  await page.locator('.nav .item', { hasText: 'Папки' }).click()
  await Promise.all([
    page.waitForResponse((r) => r.url().endsWith('/api/v1/prefs') && r.request().method() === 'PUT'),
    page.locator('[data-test=archive-structure]').selectOption('month'),
  ])
  await page.getByRole('button', { name: 'Почта' }).first().click()
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/messages?folder=INBOX')),
    page.locator('.folder', { hasText: 'Входящие' }).click(),
  ])
  await page.locator('.item').first().click()
  await Promise.all([
    page.waitForResponse((r) => r.url().endsWith('/api/v1/messages/archive') && r.ok()),
    page.locator('section.reading').getByRole('button', { name: 'В архив' }).click(),
  ])
  const now = new Date()
  const ym = `${now.getFullYear()}/${String(now.getMonth() + 1).padStart(2, '0')}`
  const monthFolder = pane.locator(`.folder[data-folder$="/${ym}"]`)
  await monthFolder.waitFor()
  // Год и месяц — внутри «Архива», сразу под ним.
  const order = await pane.locator('.folder').evaluateAll((els) => els.map((e) => e.dataset.folder))
  const ai = order.findIndex((n) => n?.endsWith(`/${ym}`))
  if (!order[ai - 1]?.endsWith(`/${now.getFullYear()}`)) throw new Error(`порядок папок: ${order.join(', ')}`)
  const yearFolder = pane.locator(`.folder[data-folder="${order[ai - 1]}"]`)
  await shot('16-archive-tree')
  await yearFolder.click({ button: 'right' })
  page.once('dialog', (d) => d.accept())
  await page.locator('[data-test=folder-menu]').getByRole('menuitem', { name: 'Удалить папку' }).click()
  await monthFolder.waitFor({ state: 'detached' })
  await page.getByRole('button', { name: 'Настройки' }).first().click()
  await page.locator('.nav .item', { hasText: 'Папки' }).click()
  await Promise.all([
    page.waitForResponse((r) => r.url().endsWith('/api/v1/prefs') && r.request().method() === 'PUT'),
    page.locator('[data-test=archive-structure]').selectOption(''),
  ])
  await page.getByRole('button', { name: 'Почта' }).first().click()
  step('«В архив» разложил письмо в папку по месяцу')

  // Важность и уведомление о прочтении: письмо самому себе.
  const mdnSubject = `Важное e2e ${stamp}`
  await page.locator('.folders').getByRole('button', { name: 'Новое письмо' }).click()
  const mdlg = page.getByRole('dialog').last()
  await mdlg.locator('.chips input').first().fill('bob@test.local')
  await mdlg.locator('.field input').last().fill(mdnSubject)
  await mdlg.locator('.ProseMirror').click()
  await page.keyboard.type('Прошу подтвердить прочтение.')
  await mdlg.locator('[data-test=compose-options]').click()
  await mdlg.locator('[data-test=compose-priority]').selectOption('1')
  await mdlg.locator('[data-test=compose-mdn]').check()
  await shot('14-compose-options')
  await mdlg.getByRole('button', { name: 'Отправить' }).click()
  await page.getByText('Письмо отправлено').last().waitFor()
  const mdnItem = page.locator('.item', { hasText: mdnSubject })
  await mdnItem.waitFor({ timeout: 10000 })
  if (!(await mdnItem.locator('.prio').count())) throw new Error('нет отметки важности в списке')
  await mdnItem.click()
  await page.locator('[data-test=mdn-banner]').waitFor()
  await page.locator('section.reading .prio.high').waitFor()
  await shot('15-mdn-banner')
  await Promise.all([
    page.waitForResponse((r) => r.url().endsWith('/api/v1/messages/mdn') && r.ok()),
    page.locator('[data-test=mdn-banner]').getByRole('button', { name: 'Отправить' }).click(),
  ])
  await page.locator('[data-test=mdn-banner]').waitFor({ state: 'detached' })
  await page.locator('.item', { hasText: `Прочитано: ${mdnSubject}` }).waitFor({ timeout: 10000 })
  step('важность видна, уведомление о прочтении отправлено и пришло')

  await page.locator('[data-test=print]').click()
  await page.locator('iframe[sandbox="allow-same-origin allow-modals"]').waitFor({ state: 'attached' })
  step('печать: готовится отдельный документ письма')

  // Шаблон ответа и картинка в тексте.
  const tplName = `e2e шаблон ${stamp}`
  await page.getByRole('button', { name: 'Настройки' }).first().click()
  await page.locator('.nav .item', { hasText: 'Шаблоны ответов' }).click()
  await page.locator('[data-test=template-new]').click()
  await page.locator('[data-test=template-name]').fill(tplName)
  await page.locator('form.card .ProseMirror').click()
  await page.keyboard.type('Текст из шаблона e2e.')
  await page.locator('form.card').getByRole('button', { name: 'Сохранить' }).click()
  await page.locator(`.row[data-template="${tplName}"]`).waitFor()
  await page.getByRole('button', { name: 'Почта' }).first().click()

  const imgSubject = `Картинка e2e ${stamp}`
  await page.locator('.folders').getByRole('button', { name: 'Новое письмо' }).click()
  const idlg = page.getByRole('dialog').last()
  await idlg.locator('.chips input').first().fill('bob@test.local')
  await idlg.locator('.field input').last().fill(imgSubject)
  await idlg.locator('.ProseMirror').click()
  await idlg.locator('[data-test=compose-templates]').click()
  await idlg.locator(`[data-template="${tplName}"]`).click()
  // Красный квадрат 40×40 — нарисован в браузере, чтобы не держать файл в репозитории.
  const png = Buffer.from(await page.evaluate(() => {
    const c = document.createElement('canvas'); c.width = 40; c.height = 40
    const g = c.getContext('2d'); g.fillStyle = '#d33'; g.fillRect(0, 0, 40, 40)
    return c.toDataURL('image/png').split(',')[1]
  }), 'base64')
  await idlg.locator('[data-test=inline-image]').setInputFiles({ name: 'square.png', mimeType: 'image/png', buffer: png })
  await idlg.locator('.ProseMirror img').waitFor()
  await shot('17-compose-image-template')
  await idlg.getByRole('button', { name: 'Отправить' }).click()
  await page.getByText('Письмо отправлено').last().waitFor()
  await page.locator('.item', { hasText: imgSubject }).waitFor({ timeout: 10000 })
  await page.locator('.item', { hasText: imgSubject }).click()
  const ibody = page.frameLocator('iframe.body')
  await ibody.locator('img').waitFor()
  const iw = await ibody.locator('img').evaluate((img) => img.complete ? img.naturalWidth : new Promise((r) => { img.onload = () => r(img.naturalWidth) }))
  if (iw !== 40) throw new Error(`картинка в письме: ширина ${iw}`)
  if (!(await ibody.locator('body').innerText()).includes('Текст из шаблона e2e.')) throw new Error('нет текста шаблона')
  if (await page.locator('section.reading .file', { hasText: 'square.png' }).count()) throw new Error('картинка из текста попала во вложения')
  await shot('18-inline-image')
  step('шаблон вставлен, картинка ушла в тексте письма (cid) и показалась')

  // Цепочки: письмо себе и ответ на него сворачиваются в одну строку.
  const thrSubject = `Цепочка e2e ${stamp}`
  await page.locator('.folders').getByRole('button', { name: 'Новое письмо' }).click()
  const tdlg = page.getByRole('dialog').last()
  await tdlg.locator('.chips input').first().fill('bob@test.local')
  await tdlg.locator('.field input').last().fill(thrSubject)
  await tdlg.locator('.ProseMirror').click()
  await page.keyboard.type('Первое письмо цепочки.')
  await tdlg.getByRole('button', { name: 'Отправить' }).click()
  await page.getByText('Письмо отправлено').last().waitFor()
  await page.locator('.item', { hasText: thrSubject }).first().waitFor({ timeout: 10000 })
  await page.locator('.item', { hasText: thrSubject }).first().click()
  await page.locator('section.reading').getByRole('button', { name: 'Ответить' }).click()
  const rdlg = page.getByRole('dialog').last()
  await rdlg.locator('.ProseMirror').click()
  await page.keyboard.type('Ответ в цепочке.')
  await rdlg.getByRole('button', { name: 'Отправить' }).click()
  await page.getByText('Письмо отправлено').last().waitFor()
  await page.locator('.item', { hasText: `Re: ${thrSubject}` }).waitFor({ timeout: 10000 })

  if (await page.locator('[data-test=threads-switch]').getAttribute('aria-checked') === 'true') {
    await Promise.all([page.waitForResponse((r) => r.url().includes('/api/v1/messages?')), page.locator('[data-test=threads-switch]').click()])
  }
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('threads=1')),
    page.locator('[data-test=threads-switch]').click(),
  ])
  const thrRow = page.locator('.item', { hasText: `Re: ${thrSubject}` })
  await thrRow.locator('[data-test=thread-toggle]', { hasText: '2' }).waitFor()
  if (await page.locator('.item', { hasText: thrSubject }).count() !== 1) throw new Error('цепочка не свернулась в одну строку')
  await thrRow.locator('[data-test=thread-toggle]').click()
  await page.locator('.item.member', { hasText: 'Re: ' + thrSubject }).waitFor()
  if (await page.locator('.item', { hasText: thrSubject }).count() !== 2) throw new Error('развёрнутая цепочка: не два письма')
  await shot('19-threads')
  // Свернуть и удалить цепочку целиком.
  await page.locator('.item', { hasText: thrSubject }).first().locator('[data-test=thread-toggle]').click()
  await page.locator('.item', { hasText: thrSubject }).first().click()
  await page.keyboard.press('Delete')
  await page.getByText('Перемещено в корзину').last().waitFor()
  await page.locator('.item', { hasText: thrSubject }).waitFor({ state: 'detached' })
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/messages?') && !r.url().includes('threads=1')),
    page.locator('[data-test=threads-switch]').click(),
  ])
  if (await page.locator('.item', { hasText: thrSubject }).count()) throw new Error('письма цепочки остались во «Входящих»')
  step('цепочка свернулась, развернулась и удалилась целиком')

  // mbox: загрузить два письма в новую папку, скачать её обратно.
  const mboxFolder = `e2e mbox ${stamp}`
  page.once('dialog', (d) => d.accept(mboxFolder))
  await page.locator('[data-test=folder-new]').click()
  const mf = pane.locator(`.folder[data-folder="${mboxFolder}"]`)
  await mf.waitFor()
  await mf.click({ button: 'right' })
  const [chooser] = await Promise.all([
    page.waitForEvent('filechooser'),
    page.locator('[data-test=folder-menu] [data-test=folder-import]').click(),
  ])
  await chooser.setFiles({ name: 'old.mbox', mimeType: 'application/mbox', buffer: Buffer.from(
    'From x@test.local Mon Jan  2 10:00:00 2023\nFrom: x@test.local\nSubject: Из mbox один\n\nТекст\n\n' +
    'From y@test.local Tue Jan  3 10:00:00 2023\nFrom: y@test.local\nSubject: Из mbox два\nStatus: RO\n\nТекст 2\n') })
  await page.getByText('Загружено писем: 2').waitFor()
  await mf.click()
  await page.locator('.item', { hasText: 'Из mbox один' }).waitFor()
  await page.locator('.item', { hasText: 'Из mbox два' }).waitFor()
  await mf.click({ button: 'right' })
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.locator('[data-test=folder-menu] [data-test=folder-export]').click(),
  ])
  const mboxText = (await import('node:fs')).readFileSync(await download.path(), 'utf8')
  if (download.suggestedFilename() !== `${mboxFolder}.mbox` || (mboxText.match(/^From /gm) ?? []).length !== 2) {
    throw new Error(`выгрузка mbox: ${download.suggestedFilename()}\n${mboxText}`)
  }
  await mf.click({ button: 'right' })
  page.once('dialog', (d) => d.accept())
  await page.locator('[data-test=folder-menu]').getByRole('menuitem', { name: 'Удалить папку' }).click()
  await mf.waitFor({ state: 'detached' })
  step('mbox: два письма загружены в папку и скачаны обратно')

  // Орфография (на стенде engine: hunspell): подчёркивание, исправление, личный словарь, смена словаря.
  const spellOn = (await (await page.request.get(`${BASE}/api/v1/auth/me`)).json()).features.spellcheck.engine
  if (spellOn === 'hunspell') {
    await page.locator('.folders').getByRole('button', { name: 'Новое письмо' }).click()
    const sdlg = page.getByRole('dialog').last()
    await sdlg.locator('.ProseMirror').click()
    await page.keyboard.type('Превет, как дела? Сквербол ')
    const errs = sdlg.locator('.ProseMirror .spell-error')
    // waitForFunction запрещён CSP страницы — опрашиваем через локатор.
    const waitErrs = async (ok, what) => {
      for (let i = 0; i < 50; i++) { if (ok(await errs.count())) return; await page.waitForTimeout(200) }
      throw new Error(`орфография: ${what}, подчёркнуто ${await errs.count()}`)
    }
    await waitErrs((n) => n === 2, 'ожидалось 2 ошибки')
    await shot('21-spell-errors')
    await errs.filter({ hasText: 'Превет' }).click({ button: 'right' })
    await page.locator('[data-test=spell-menu]').getByRole('menuitem', { name: 'Привет', exact: true }).click()
    await waitErrs((n) => n === 1, 'после исправления')
    if (!(await sdlg.locator('.ProseMirror').innerText()).startsWith('Привет, как дела?')) throw new Error('исправление не применилось')
    await errs.filter({ hasText: 'Сквербол' }).click({ button: 'right' })
    await page.locator('[data-test=spell-add]').click()
    await waitErrs((n) => n === 0, 'ожидалось 0')
    // Только английский словарь: русские слова становятся ошибками.
    await sdlg.locator('[data-test=spell-lang]').click()
    await page.locator('[data-test=spell-lang-menu] [data-dict=en_US]').click()
    await waitErrs((n) => n >= 3, 'только en_US')
    await sdlg.locator('[data-test=spell-lang]').click()
    await page.locator('[data-test=spell-lang-menu]').getByRole('menuitemradio', { name: 'Все словари' }).click()
    await waitErrs((n) => n === 0, 'ожидалось 0')
    await sdlg.getByTitle('Удалить черновик').click()
    // Слово убираем из личного словаря через настройки.
    await page.getByRole('button', { name: 'Настройки' }).first().click()
    await page.locator('.nav .item', { hasText: 'Орфография' }).click()
    await page.locator('[data-test=spell-word]', { hasText: 'Сквербол' }).getByRole('button').click()
    await page.locator('[data-test=spell-word]', { hasText: 'Сквербол' }).waitFor({ state: 'detached' })
    await page.getByRole('button', { name: 'Почта' }).first().click()
    step('орфография: ошибки подчёркнуты, исправлены, слово в личном словаре, смена словаря')
  }

  await page.getByRole('button', { name: 'Настройки' }).first().click()
  await page.locator('.nav .item', { hasText: 'Шаблоны ответов' }).click()
  page.once('dialog', (d) => d.accept())
  await page.locator(`.row[data-template="${tplName}"]`).getByRole('button', { name: 'Удалить' }).click()
  await page.locator(`.row[data-template="${tplName}"]`).waitFor({ state: 'detached' })
  await page.getByRole('button', { name: 'Почта' }).first().click()

  {
    const summary = `Планёрка e2e ${Date.now()}`
    const org = await sendInvite(summary)
    await Promise.all([
      page.waitForResponse((r) => r.url().includes('/api/v1/messages?folder=INBOX')),
      page.locator('.folder', { hasText: 'Входящие' }).click(),
    ])
    await page.locator('.item', { hasText: summary }).first().click()
    const card = page.locator('[data-test=invite-card]')
    await card.waitFor()
    for (const want of [summary, 'Приглашение на встречу', 'Переговорная 3', 'Алиса', 'приняли: 1', 'Кэрол']) {
      if (!(await card.textContent()).includes(want)) throw new Error(`в карточке нет «${want}»`)
    }
    await shot('24-invite')
    await card.locator('[data-test=rsvp-accept]').click()
    await page.getByText('ответ отправлен').waitFor()
    await card.locator('[data-test=rsvp-accept].on').waitFor()
    await card.getByText('Вы ответили: Принято').waitFor()
    await card.getByText('приняли: 2').waitFor() // свой ответ учтён в списке участников
    // Ответ METHOD:REPLY пришёл организатору.
    let got = false
    for (let i = 0; i < 50 && !got; i++) {
      const r = await org.api.get(`/api/v1/messages?folder=INBOX&q=${encodeURIComponent('Принято: ' + summary)}`)
      got = (await r.json()).messages.length > 0
      if (!got) await page.waitForTimeout(200)
    }
    await org.api.post('/api/v1/auth/logout', { headers: { 'X-CSRF-Token': org.csrf } })
    await org.api.dispose()
    if (!got) throw new Error('ответ на приглашение не дошёл организатору')
    await shot('25-invite-accepted')
    step('приглашение: карточка встречи, «Принять», ответ REPLY у организатора')
  }

  // Вход из панели по подписанной одноразовой ссылке (adapters.panel), если он включён.
  const panelKey = process.env.PANEL_KEY ?? '../deploy/devstand/panel-sso.key'
  if (existsSync(panelKey)) {
    const key = readFileSync(panelKey, 'utf8').trim()
    const mailbox = USER1.includes('@') ? USER1 : `${USER1}@test.local`
    const payload = 'v1.' + Buffer.from(JSON.stringify({ sub: mailbox, exp: Math.floor(Date.now() / 1000) + 60, jti: randomUUID() })).toString('base64url')
    const link = `${BASE}/sso/panel?token=${payload}.${createHmac('sha256', key).update(payload).digest('base64url')}`
    const sso = await browser.newContext({ viewport: { width: 1280, height: 800 }, locale: 'ru-RU' })
    await sso.addInitScript(() => localStorage.setItem('wm.lang', 'ru'))
    const sp = await sso.newPage()
    sp.on('pageerror', (e) => errors.push('sso: ' + e.message))
    await sp.goto(link)
    if (!sp.url().includes('/sso/panel')) { // адаптер выключен — ссылку обработает SPA
      await sp.locator('.item').first().waitFor()
      await sp.getByRole('button', { name: 'Настройки' }).first().click()
      await sp.locator('.nav .item').first().waitFor()
      if (await sp.locator('.nav .item', { hasText: 'Пароль' }).isVisible()) {
        await sp.locator('.nav .item', { hasText: 'Пароль' }).click()
        await sp.locator('[data-test=pw-sso]').waitFor()
        if (await sp.locator('[data-test=pw-current]').count()) throw new Error('после входа из панели спрашивают текущий пароль')
      }
      await sp.screenshot({ path: `${SHOTS}/26-panel-sso.png` })
      // Повтор той же ссылки не пускает.
      const again = await (await browser.newContext()).newPage()
      const res = await again.goto(link)
      if (res.status() !== 403) throw new Error(`повтор ссылки входа: HTTP ${res.status()}`)
      await again.context().close()
      step('вход из панели по одноразовой ссылке, смена пароля без текущего')

      // Панель передаёт язык и тему — они важнее выбора, сохранённого в браузере.
      const p2 = 'v1.' + Buffer.from(JSON.stringify({ sub: mailbox, exp: Math.floor(Date.now() / 1000) + 60, jti: randomUUID(), lang: 'ru', theme: 'dark' })).toString('base64url')
      const ctx2 = await browser.newContext({ viewport: { width: 1280, height: 800 }, colorScheme: 'light' })
      await ctx2.addInitScript(() => {
        if (!sessionStorage.getItem('seeded')) {
          localStorage.setItem('wm.lang', 'en'); localStorage.setItem('wm.theme', 'light'); sessionStorage.setItem('seeded', '1')
        }
      })
      const pp = await ctx2.newPage()
      pp.on('pageerror', (e) => errors.push('sso prefs: ' + e.message))
      await pp.goto(`${BASE}/sso/panel?token=${p2}.${createHmac('sha256', key).update(p2).digest('base64url')}`)
      await pp.locator('.item').first().waitFor()
      const got = await pp.evaluate(() => ({
        lang: document.documentElement.lang, theme: document.documentElement.dataset.theme, search: location.search,
        stored: localStorage.getItem('wm.lang') + '/' + localStorage.getItem('wm.theme'),
        cookie: document.cookie.includes('wm_lang=ru'),
      }))
      if (got.lang !== 'ru' || got.theme !== 'dark' || got.search !== '' || got.stored !== 'ru/dark' || !got.cookie) {
        throw new Error('язык и тема из панели: ' + JSON.stringify(got))
      }
      if (!(await pp.getByRole('button', { name: 'Настройки' }).first().isVisible())) throw new Error('интерфейс не на русском после входа из панели')
      await pp.screenshot({ path: `${SHOTS}/27-panel-sso-prefs.png` })
      await ctx2.close()
      step('вход из панели с языком и темой панели')
    }
    await sso.close()
  }

  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/messages?folder=INBOX')),
    page.locator('.folder', { hasText: 'Входящие' }).click(),
  ])
  await page.locator('.item').first().waitFor()
  const victim = await page.locator('.item').last().getAttribute('data-uid')
  await page.locator('.item').last().click()
  await page.keyboard.press('Delete')
  await page.getByText('Перемещено в корзину').waitFor()
  // Список могли дозагрузить (во «Входящих» больше страницы) — проверяем само письмо.
  await page.locator(`.item[data-uid="${victim}"]`).waitFor({ state: 'detached' })
  step('удаление в корзину')

  await page.evaluate(() => { localStorage.setItem('wm.theme', 'dark') })
  await page.reload()
  await page.locator('.item').first().click()
  await page.frameLocator('iframe.body').locator('body').waitFor()
  await shot('05-dark')
  step('тёмная тема')

  await page.setViewportSize({ width: 390, height: 800 })
  await page.reload()
  await page.locator('.item').first().waitFor()
  await shot('06-mobile-list')
  await page.locator('.burger').click()
  await page.locator('.folder', { hasText: 'Отправленные' }).click()
  await page.getByRole('heading', { name: 'Отправленные' }).waitFor()
  await page.locator('.burger').click()
  await page.locator('.folder', { hasText: 'Входящие' }).click()
  await page.getByRole('heading', { name: 'Входящие' }).waitFor()
  await page.locator('.item').first().click()
  await page.frameLocator('iframe.body').locator('body').waitFor()
  await shot('07-mobile-read')
  step('мобильная раскладка')

  if (errors.length) throw new Error('ошибки в консоли:\n' + errors.join('\n'))
  console.log('OK')
} catch (e) {
  await shot('zz-failure').catch(() => {})
  console.error('FAIL:', e.message)
  process.exitCode = 1
} finally {
  await browser.close()
}
