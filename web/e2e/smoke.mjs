// Сквозной smoke-тест против живого стенда (deploy/devstand).
// Запуск: BASE=http://127.0.0.1:8080 node e2e/smoke.mjs
// Переменные: CHROMIUM (путь к браузеру), SHOTS (каталог для скриншотов),
// USER1/USER2/PASS — учётки стенда.
import { chromium, request } from 'playwright-core'
import { mkdirSync } from 'node:fs'

const BASE = process.env.BASE ?? 'http://127.0.0.1:8080'
const SHOTS = process.env.SHOTS ?? 'e2e/out'
const USER1 = process.env.USER1 ?? 'bob'
const USER2 = process.env.USER2 ?? 'alice@test.local'
const PASS = process.env.PASS ?? 'Test12345!'
mkdirSync(SHOTS, { recursive: true })

const browser = await chromium.launch({ executablePath: process.env.CHROMIUM ?? '/usr/bin/chromium', args: ['--no-sandbox'] })
const errors = []
const ctx = await browser.newContext({ viewport: { width: 1440, height: 860 }, locale: 'ru-RU' })
const page = await ctx.newPage()
page.on('pageerror', (e) => errors.push(e.message))
// 401 до входа и при неверном пароле — ожидаемые ответы API, не ошибки.
page.on('console', (m) => { if (m.type() === 'error' && !m.text().includes('status of 401')) errors.push(m.text()) })
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

try {
  await seed()
  step('тестовые письма отправлены через API')
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
  await compose.locator('input[type=file]').setInputFiles({ name: 'отчёт.txt', mimeType: 'text/plain', buffer: Buffer.from('данные') })
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

  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/messages?folder=INBOX')),
    page.locator('.folder', { hasText: 'Входящие' }).click(),
  ])
  await page.locator('.item').first().waitFor()
  const before = await page.locator('.item').count()
  await page.locator('.item').last().click()
  await page.keyboard.press('Delete')
  await page.getByText('Перемещено в корзину').waitFor()
  const after = await page.locator('.item').count()
  if (after !== before - 1) throw new Error(`после удаления ${after}, ожидалось ${before - 1}`)
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
