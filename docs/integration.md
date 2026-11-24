# Интеграция с панелью хостинга

Адаптер `adapters/panel` входит в стандартный бинарь и даёт панели два механизма:

- **Вход в почту без пароля (SSO).** Панель подписывает одноразовую ссылку и отправляет по ней браузер. Webmail входит в IMAP и SMTP мастер-учёткой Dovecot, поэтому пароль ящика панели знать не нужно. Панель может передать свои язык и тему, и почта откроется так же (раздел 3).
- **Служебный API.** Через него панель завершает сессии ящика и удаляет данные, которые webmail хранит о нём.

Смена пароля из webmail в панель описана отдельно: [password.md](password.md) (драйвер `http` или `exec`).

## 1. Мастер-учётка Dovecot

Webmail входит как `ящик*мастер` через SASL PLAIN с authzid. Ящик при этом всё равно проверяется в обычном passdb, поэтому несуществующий или заблокированный ящик не откроется.

Dovecot 2.4:

```
passdb master {
  driver = passwd-file
  master = yes
  passwd_file_path = /etc/dovecot/master-users
  result_success = continue
}
```

Dovecot 2.3:

```
passdb {
  driver = passwd-file
  master = yes
  args = /etc/dovecot/master-users
  pass = yes
}
```

Создать файл мастер-учёток:

```sh
echo "webmail-master:$(doveadm pw -s BLF-CRYPT)::::::" > /etc/dovecot/master-users
chown root:dovecot /etc/dovecot/master-users && chmod 640 /etc/dovecot/master-users
```

Тот же пароль, одной строкой, кладётся в `/etc/webmail/master.pass` с правами 0600 и владельцем `webmail`.

Postfix с `smtpd_sasl_type = dovecot` принимает такой вход без дополнительной настройки. `reject_sender_login_mismatch` проверяет именно ящик, а не мастер-учётку: на стенде это проверено. ManageSieve у Dovecot тоже понимает authzid.

## 2. Конфиг webmail

```yaml
imap:
  impersonation:
    enabled: true
    master_user: webmail-master
    master_password_file: /etc/webmail/master.pass
    allowed_sources: [panel]      # имя источника адаптера panel
    apply_to: [smtp, sieve]       # без smtp из SSO-сессии нельзя отправлять письма
password:
  sso_window: 10m                 # см. раздел 5; 0 — всегда спрашивать текущий пароль
adapters:
  panel:
    enabled: true
    secret_file: /etc/webmail/panel-sso.key       # общий с панелью ключ, 0600, ≥ 32 байт
    max_ttl: 2m                                   # токен с более долгим сроком отвергается
    domains: [example.com, example.org]           # пусто — любые (и login.allowed_domains)
    error_url: https://panel.example.com/mail?sso=failed  # необязательно
    api:
      token_file: /etc/webmail/panel-api.token    # без него API выключен
      allowed_ips: [127.0.0.1/32, ::1/128]        # по умолчанию только loopback
```

Сгенерировать ключи:

```sh
cd /etc/webmail && umask 077
openssl rand -hex 32 > panel-sso.key
openssl rand -hex 32 > panel-api.token
chown webmail panel-sso.key panel-api.token
```

Ключ — это содержимое файла как текст, без завершающего перевода строки. В примере выше это 64 hex-символа, и именно эта строка используется как ключ HMAC, а не 32 байта, которые она кодирует. Панель хранит ту же строку.

Проверить конфиг: `webmail config check`.

## 3. Ссылка входа

Токен:

```
v1.<payload>.<sig>
payload = base64url(JSON), без "="
sig     = base64url(HMAC-SHA256(ключ, "v1." + payload)), без "="
```

JSON:

| Поле | Что это |
| --- | --- |
| `sub` | ящик `local@domain`, регистр не важен |
| `exp` | unix-время окончания, обычно сейчас + 60 с; не дальше `max_ttl` |
| `jti` | случайная строка 16–128 символов; второй раз токен не примут |
| `lang` | необязательно: язык интерфейса, `en` или `ru` |
| `theme` | необязательно: тема, `light`, `dark` или `system` (как в системе пользователя) |

### Язык и тема панели

Чтобы почта открылась на языке панели и в её цветовом режиме, передайте в токене
`lang` и `theme`. Они подписаны вместе с остальными полями, поэтому подменить их по
дороге нельзя.

- Значения панели важнее выбора, сохранённого в браузере. Почта запомнит их как выбор
  пользователя: при следующем входе по паролю в этом браузере будут те же язык и тема.
  Передавайте поля при каждом входе, тогда смена языка или темы в панели сразу дойдёт до почты.
- Если поле не передано, остаётся то, что выбрано в браузере. Если там ничего не выбрано,
  действуют `ui.default_language` и `ui.theme`.
- Неизвестное значение или язык, выключенный в `ui.languages`, не считается ошибкой:
  вход проходит, а поле пропускается. Регистр и пробелы по краям не важны. Регион
  (`ru-RU`, `en-US`) не нужен: передавайте код языка.
- Язык действует и для писем, которые формирует сервер: уведомлений о прочтении,
  ответов на приглашения, имён правил «Автоответ»/«Пересылка» в фильтрах.

Как это работает: после входа webmail перенаправляет браузер на `/?lang=…&theme=…`, клиент
применяет значения и убирает их из адреса. Те же параметры понимает любая ссылка на почту,
например `https://mail.example.com/?lang=ru` с экрана входа панели без SSO.

Браузер отправляется либо `GET /sso/panel?token=…`, либо формой `POST /sso/panel` с полем `token`. Форма лучше: токен не попадёт в журналы прокси и историю браузера. При успехе webmail ставит cookie сессии и перенаправляет на `/` (303), а если в токене есть `lang` или `theme`, то на `/?lang=…&theme=…`. При ошибке отвечает 403 со страницей «ссылка недействительна» на языке пользователя (cookie `wm_lang`, иначе `Accept-Language`, иначе `ui.default_language`) или перенаправляет на `error_url`. Причина отказа пишется в журнал с `audit=true`: подпись, срок, повтор или домен.

Часы панели и почтового сервера могут расходиться до 30 секунд.

PHP:

```php
// $lang — 'en' | 'ru', $theme — 'light' | 'dark' | 'system'; null — не передавать.
function webmail_sso_token(string $mailbox, string $key, ?string $lang = null, ?string $theme = null, int $ttl = 60): string {
    $b64 = fn(string $s) => rtrim(strtr(base64_encode($s), '+/', '-_'), '=');
    $payload = 'v1.' . $b64(json_encode(array_filter([
        'sub' => $mailbox, 'exp' => time() + $ttl, 'jti' => bin2hex(random_bytes(16)),
        'lang' => $lang, 'theme' => $theme,
    ])));
    return $payload . '.' . $b64(hash_hmac('sha256', $payload, $key, true));
}
// $key = trim(file_get_contents('/etc/panel/webmail-sso.key'));
```

```html
<form method="post" action="https://mail.example.com/sso/panel" target="_blank">
  <input type="hidden" name="token" value="<?= htmlspecialchars(webmail_sso_token($mailbox, $key, $user->lang, $user->theme)) ?>">
  <button>Открыть почту</button>
</form>
```

Shell (для проверки):

```sh
b64url() { base64 -w0 | tr '+/' '-_' | tr -d '='; }
payload="v1.$(printf '{"sub":"%s","exp":%d,"jti":"%s","lang":"ru","theme":"dark"}' bob@example.com $(( $(date +%s) + 60 )) "$(openssl rand -hex 16)" | b64url)"
sig=$(printf '%s' "$payload" | openssl dgst -sha256 -hmac "$(cat /etc/webmail/panel-sso.key)" -binary | b64url)
echo "https://mail.example.com/sso/panel?token=$payload.$sig"
```

Go: `panel.Sign(key, panel.Claims{Sub: …, Exp: …, Jti: …, Lang: "ru", Theme: "dark"})`.

## 4. Служебный API

Все запросы — `POST` с заголовком `Authorization: Bearer <panel-api.token>`; запросы по ящику передают JSON `{"mailbox": "local@domain"}`. Принимаются только с адресов из `allowed_ips`. Адрес клиента берётся с учётом `server.trusted_proxies`, как и везде в webmail.

| Запрос | Когда вызывать | Ответ |
| --- | --- | --- |
| `/panel/v1/sessions/end` | пароль сменён в панели, ящик заблокирован или переведён на другой тариф | `{"ok":true,"ended":N}` |
| `/panel/v1/mailboxes/delete` | ящик удалён | `{"ok":true}` |
| `/panel/v1/books/stale` | в панели изменились общие контакты домена (пресет CardDAV) | `{"ok":true,"accounts":N}` |

`mailboxes/delete` завершает сессии и удаляет всё, что webmail хранит о ящике: отправителей и подписи, контакты и группы, собранные адреса, шаблоны, личный словарь, настройки и подключённые внешние адресные книги. Письма на IMAP-сервере не трогает. Вызов идемпотентен.

Ошибки: `401` — неверный токен, `403` — адрес или домен не разрешён, `400` — неверный JSON или ящик. Тело ошибки: `{"ok":false,"error":"…"}`.

```sh
curl -sX POST -H "Authorization: Bearer $(cat /etc/webmail/panel-api.token)" \
  -d '{"mailbox":"bob@example.com"}' http://127.0.0.1:8080/panel/v1/sessions/end
```

`books/stale` — запрос на весь домен, поэтому тело у него другое: `{"preset": "<id пресета>", "domain": "example.com"}`. `preset` обязателен — это `id` из `adapters.carddav.presets`. `domain` необязателен: без него помечаются учётки пресета во всех ящиках, с ним — только в ящиках `*@domain` (домен должен входить в `domains` адаптера панели, иначе `403`). Вызов сбрасывает время синхронизации учёток и их книг, и при следующем открытии контактов клиент заново скачает карточки. `N` — число затронутых учёток (ящиков, которые уже подключали этот пресет). Вызов идемпотентен.

```sh
curl -sX POST -H "Authorization: Bearer $(cat /etc/webmail/panel-api.token)" \
  -d '{"preset":"domain","domain":"example.com"}' http://127.0.0.1:8080/panel/v1/books/stale
```

Открытая вкладка с почтой после `sessions/end` получает 401 на следующем запросе и показывает форму входа. Поток новых писем закрывается в течение 25 секунд.

## 5. Смена пароля после входа из панели

Пользователь, вошедший из панели, мог не знать пароль ящика. Поэтому в течение `password.sso_window` (по умолчанию 10 минут) после входа он может сменить пароль в «Настройки → Пароль», не вводя текущий. Для этого должны выполняться два условия:

- драйвер умеет менять пароль без текущего (`ext.PasswordCaps.SSOWithoutCurrent`);
- у встроенных драйверов `exec` и `http` не стоит `verifies_current: true`.

Драйвер получает запрос с `"sso": true` и без `current`. Остальные проверки остаются в силе: политика пароля, утечки HIBP, лимит попыток. После окна текущий пароль спрашивается как обычно.

## 6. Адресные книги CardDAV при входе из панели

Пресет CardDAV с `password: session` берёт пароль ящика из сессии. При входе из панели (impersonation) пароля ящика у webmail нет, поэтому такие книги обычно не синхронизируются. Исключение — локальный сервер: если `url` пресета указывает на `127.0.0.1`, `localhost` или `::1`, webmail подставляет пароль мастер-учётки (`imap.impersonation.master_password_file`). Так работает, например, Radicale на той же машине с аутентификацией через Dovecot: он принимает мастер-пароль так же, как IMAP. Для удалённых серверов и учёток, которые пользователь подключил сам, мастер-пароль не отправляется никогда.

## Ограничения

- Список использованных `jti` хранится в памяти процесса. С несколькими экземплярами webmail за балансировщиком одноразовость гарантирована только в пределах одного экземпляра. Срок токена (`max_ttl`) ограничивает окно повтора, но лучше держать ссылку короткой и отправлять её формой.
- Сессии тоже живут в памяти (`session.store: memory`): после перезапуска webmail пользователям нужно снова войти из панели.
