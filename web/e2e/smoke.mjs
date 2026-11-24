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

// Готовим данные: USER2 отправляет USER1 свежие письма через API клиента.
async function seed() {
  const api = await request.newContext({ baseURL: BASE })
  const login = await api.post('/api/v1/auth/login', { data: { username: USER2, password: PASS } })
  if (!login.ok()) throw new Error(`seed login: ${login.status()}`)
  const { csrf } = await login.json()
  const to = USER1.includes('@') ? USER1 : `${USER1}@test.local`
  for (let i = 1; i <= 3; i++) {
    const draft = {
      to: [{ email: to }], subject: `Письмо для e2e №${i}`,
      html: `<p>Привет! Это письмо <b>№${i}</b>.</p><img src="https://example.com/pixel.png"><script>alert(1)</script>`,
    }
    const r = await api.post('/api/v1/send', {
      headers: { 'X-CSRF-Token': csrf },
      multipart: i === 1
        ? { draft: JSON.stringify(draft), attachments: { name: 'заметка.txt', mimeType: 'text/plain', buffer: Buffer.from('hello') } }
        : { draft: JSON.stringify(draft) },
    })
    if (!r.ok()) throw new Error(`seed send: ${r.status()} ${await r.text()}`)
  }
  await api.post('/api/v1/auth/logout', { headers: { 'X-CSRF-Token': csrf } })
  await api.dispose()
}

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
  // Курсор уже стоит в начале редактора, над цитатой.
  await page.keyboard.type('Проверка ответа из e2e. ')
  const quoteText = await page.getByRole('dialog').locator('.editor blockquote').innerText()
  if (quoteText.includes('Проверка ответа')) throw new Error('ответ напечатан внутри цитаты')
  await shot('04-reply')
  await page.getByRole('dialog').getByRole('button', { name: 'Отправить' }).click()
  await page.getByText('Письмо отправлено').waitFor()
  step('ответ отправлен')

  await page.locator('.folders').getByRole('button', { name: 'Новое письмо' }).click()
  const compose = page.getByRole('dialog').last()
  await compose.locator('.chips input').first().fill(USER2)
  await compose.locator('.field input').last().fill('Письмо из e2e-теста')
  await compose.locator('.editor').click()
  await page.keyboard.type('Привет! Это проверка отправки.')
  await compose.locator('input[type=file]').setInputFiles({ name: 'отчёт.txt', mimeType: 'text/plain', buffer: Buffer.from('данные') })
  await compose.getByRole('button', { name: 'Отправить' }).click()
  await page.getByText('Письмо отправлено').last().waitFor()
  step('новое письмо с вложением отправлено')

  await page.locator('.folder', { hasText: 'Отправленные' }).click()
  await page.locator('.item', { hasText: 'Письмо из e2e-теста' }).first().waitFor()
  step('копия в «Отправленных»')

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
