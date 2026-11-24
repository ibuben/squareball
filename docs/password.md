# Смена пароля

Пользователь меняет пароль в «Настройки → Пароль». Ядро само проверяет новый
пароль по политике и текущий пароль входом в IMAP, а запись нового пароля
поручает **драйверу** из `password.driver`:

| Драйвер | Когда подходит |
| --- | --- |
| `none` | По умолчанию: раздела «Пароль» в интерфейсе нет |
| `exec` | Внешняя команда на этом же сервере: passwd-file Dovecot (встроенный помощник), SQL, LDAP, свой скрипт |
| `http` | API панели хостинга или сервиса учётных записей |
| имя адаптера | Драйвер регистрирует адаптер (например, интеграция с панелью) |

## Что проверяет ядро

Порядок обработки `POST /api/v1/account/password` (`{"current": "...", "new": "..."}`):

1. **Политика** (`password.policy`), без похода к серверам. Отказы на этом шаге
   лимит попыток не тратят:
   - длина `min_length`–`max_length` символов (по умолчанию 10–128);
   - `require_classes` из 4 групп: строчные, заглавные, цифры, прочие (по умолчанию 3);
   - `forbid_mailbox` — пароль не содержит имя ящика до `@` (по умолчанию включено);
   - управляющие символы и переводы строк запрещены всегда;
   - новый пароль не совпадает с текущим.

   Драйвер может ужесточить правила (`PasswordCaps.Policy`): берётся большее
   из минимумов, меньшее из максимумов, плюс его запрещённые символы.
2. **Лимит попыток** — тот же, что у входа (`login.rate_limit.per_user`).
3. **Текущий пароль** — входом в IMAP, если драйвер не проверяет его сам
   (`verifies_current: true`).
4. **Утечки** (`check_breached: true`, по умолчанию выключено) — Have I Been
   Pwned по k-anonymity: наружу уходят первые 5 символов SHA-1, не пароль.
   Если сервис недоступен, смена не блокируется, в журнал пишется предупреждение.
5. **Драйвер.** Запрос к драйверу не прерывается, если пользователь закрыл
   вкладку; время ограничено `timeout` драйвера.

После смены текущая сессия продолжает работать с новым паролем, остальные
сессии ящика завершаются. В аудит-лог пишется `password changed`.

Ошибки API: `password.weak` (detail: `too_short`, `too_long`, `classes`, `chars`,
`mailbox`, `same`, `breached`, `backend`), `password.current`,
`password.current_required`, `auth.locked`, `password.unavailable`, `password.disabled`.
`GET /api/v1/account/password` отдаёт итоговую политику для подсказок в форме.

## Запрос к драйверу

Оба внешних драйвера получают один и тот же JSON (поля как у хука панели):

```json
{"user": "bob@example.com", "current": "старый", "password": "новый", "remote": "203.0.113.7"}
```

`sso: true` и пустой `current` бывают только у адаптеров со входом через SSO.

## exec

```yaml
password:
  driver: exec
  exec:
    command: [/usr/local/sbin/mail-passwd, --some-flag]   # argv без оболочки, путь абсолютный
    timeout: 10s
    verifies_current: false
```

- JSON приходит в **stdin** — пароли никогда не попадают в argv и окружение
  (их видно в `/proc`). Окружение команды: только `PATH` и `LANG=C.UTF-8`.
- Ответ — кодом выхода:

  | Код | Значение |
  | --- | --- |
  | 0 | пароль сменён |
  | 2 | неверный текущий пароль |
  | 3 | новый пароль не подходит бэкенду |
  | 4 | слишком много попыток |
  | другое, тайм-аут | сервис недоступен; stdout/stderr (до 2 КБ) — в журнал |

- Смены выполняются по одной, параллельных запусков нет.
- Команда работает от пользователя `webmail` внутри песочницы systemd. Пути,
  куда ей нужно писать, открывают drop-in'ом (`ReadWritePaths=`), а для
  действий от root команда должна сама обратиться к привилегированному
  сервису (например, через `http`-драйвер к локальному агенту).

### Dovecot passwd-file: встроенный помощник

Для схемы «Postfix + Dovecot с `passdb passwd-file`» помощник уже есть в бинаре:

```yaml
password:
  driver: exec
  exec:
    command: [/usr/local/bin/webmail, passwd-file, -file, /etc/dovecot/passwd.d/users]
```

Он находит строку ящика (без учёта регистра), заменяет хеш на `{BLF-CRYPT}`
(bcrypt, cost 10) и сохраняет остальные поля (uid, gid, home, extra fields).
Файл заменяется атомарно, под `flock`, с прежними правами и группой.
`-verify-current` дополнительно сверяет текущий пароль с хешем в файле
(только для BLF-CRYPT); обычно не нужен — ядро проверяет его через IMAP.
Длиннее 72 байт bcrypt пароль не принимает — помощник отвечает кодом 3.

Две тонкости Dovecot, которые помощник учитывает:
- Dovecot перечитывает файл, только если изменились mtime (в секундах) или
  размер, а хеши bcrypt одной длины. Поэтому mtime новой версии всегда хотя бы
  на секунду больше прежнего.
- Файл проверяется не чаще раза в секунду. Помощник возвращает ответ только
  с началом следующей секунды, чтобы вход сразу после смены видел новый пароль.

Права: помощнику нужна запись в **каталог** файла (rename), Dovecot — чтение файла.

```sh
install -d -o webmail -g dovecot -m 2750 /etc/dovecot/passwd.d   # setgid: файлы остаются в группе dovecot
mv /etc/dovecot/users /etc/dovecot/passwd.d/users
chown webmail:dovecot /etc/dovecot/passwd.d/users && chmod 0640 /etc/dovecot/passwd.d/users
# в конфиге Dovecot: passwd_file_path = /etc/dovecot/passwd.d/users
install -D -m 0644 deploy/systemd/password-passwd-file.conf /etc/systemd/system/webmail.service.d/password.conf
systemctl daemon-reload && systemctl restart webmail dovecot
```

### Пример: SQL (Postfix Admin и похожие)

```sh
#!/bin/sh
# /usr/local/sbin/mail-passwd — запрос JSON в stdin, ответ кодом выхода.
set -eu
req=$(cat)
user=$(printf '%s' "$req" | jq -j .user)
# Пароль идёт в mkpasswd через stdin (-s), не через argv.
hash={BLF-CRYPT}$(printf '%s' "$req" | jq -j .password | mkpasswd -m bcrypt -s) || exit 1
case $user in *[!A-Za-z0-9@._+-]*) exit 1 ;; esac   # только безопасные символы в SQL
mysql --defaults-extra-file=/etc/webmail/mysql.cnf mail \
  -e "UPDATE mailbox SET password = '$hash', modified = NOW() WHERE username = '$user'" || exit 1
```

`user` — ящик сессии; хеш bcrypt состоит из `[./A-Za-z0-9$]`. `mkpasswd` —
из пакета `whois`; `jq -j` печатает строку без перевода строки в конце.

## http

```yaml
password:
  driver: http
  http:
    url: http://127.0.0.1:10041/webmail/password   # http — только для loopback, иначе https
    token_file: /etc/webmail/password-api.token    # Bearer, права 0600; можно без токена
    timeout: 10s
    tls_verify: true
    verifies_current: true     # API сам проверяет текущий пароль — ядро не ходит в IMAP
```

`POST` с JSON выше, `Authorization: Bearer <токен>`. Ответ — по коду HTTP:

| Код | Значение |
| --- | --- |
| 2xx | пароль сменён |
| 403 | неверный текущий пароль |
| 400, 422 | новый пароль не подходит |
| 429 | слишком много попыток |
| другое (в т. ч. 401, редиректы) | сервис недоступен; тело ответа (до 2 КБ) — в журнал |

Редиректы не выполняются (тело с паролями не уходит на другой адрес),
прокси из окружения не используется.
