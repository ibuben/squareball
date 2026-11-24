# webmail

Почтовый веб-клиент на Go: один бинарь со встроенным SPA (Vue 3), работает с любым
IMAP + SMTP submission (+ ManageSieve) сервером по своему конфигу. Интерфейс — в духе
десктопных клиентов: папки, список писем, панель чтения и карточка собеседника.

Лицензия — MIT. Интеграции с панелями хостинга подключаются как адаптеры через
публичный пакет `ext` и в ядро не входят.

## Установка

```sh
make web build                 # SPA + бинарь bin/webmail
sudo make install              # systemd-сервис webmail (deploy/install.sh)
sudo nano /etc/webmail/config.yaml && sudo systemctl restart webmail
journalctl -u webmail -f
```

`deploy/install.sh` создаёт пользователя `webmail`, `/etc/webmail/` с конфигом из примера
и секретом сессии, ставит юнит с песочницей (`systemd-analyze security webmail` — 1.4)
и запускает сервис, только если `webmail config check` проходит. Повторный запуск
обновляет бинарь и юнит, не трогая конфиг и секрет. Переменные `WEBMAIL_*` можно
положить в `/etc/webmail/webmail.env`.

Без systemd:

```sh
./bin/webmail gen-secret > session.key && chmod 600 session.key
./bin/webmail config check -config config.yaml
./bin/webmail serve -config config.yaml
```

Минимальный конфиг для обычного Postfix + Dovecot (полный пример — `deploy/config.example.yaml`):

```yaml
version: 1
server:
  public_url: https://mail.example.com
session:
  secret_file: /etc/webmail/session.key
imap:
  host: mail.example.com
  port: 993
  security: tls
smtp:
  host: mail.example.com
  port: 587
  security: starttls
```

Сервер слушает `127.0.0.1:8080`; TLS обычно завершает nginx или caddy перед ним
(адрес прокси должен быть в `server.trusted_proxies`, по умолчанию 127.0.0.1).

### Правила конфига

- Неизвестный ключ — ошибка при старте.
- Любой ключ переопределяется переменной окружения: `WEBMAIL_IMAP__HOST`,
  `WEBMAIL_SESSION__IDLE_TIMEOUT=45m`, списки — через запятую.
- Секреты — только файлами `*_file` с правами 0600; в YAML их нет.
- `webmail config show` печатает итоговый конфиг со значениями по умолчанию.
- `security: none` разрешён только для loopback-адресов.

Полный список ключей — `internal/config/config.go` (`Defaults`).

Данные пользователей (отправители, подписи, контакты) лежат в SQLite: под systemd —
`/var/lib/webmail/webmail.db`, иначе — `./webmail.db` (или путь из `storage.dsn_file`).
Фильтры включаются секцией `sieve: {enabled: true}`; хост по умолчанию — `imap.host`.

## Что уже работает

| Область | Состояние |
| --- | --- |
| Вход | Логин/пароль через IMAP, домен по умолчанию, лимиты попыток по IP и ящику |
| Сессии | Своя cookie; пароль IMAP хранится зашифрованным, ключ — частично только в cookie |
| Папки | SPECIAL-USE + имена как у Roundcube, счётчики через LIST-STATUS, вложенность |
| Список | Сортировка на сервере (SORT), поиск, фильтры, группировка по датам, мультивыбор, drag & drop в папки |
| Чтение | Санитизация HTML (bluemonday + свои правила), блокировка внешних картинок, cid-картинки, вложения, исходник .eml |
| Отправка | Новое, ответ, ответ всем, пересылка, вложения, копия в «Отправленные», флаги \Answered / $Forwarded |
| Редактор | TipTap: форматирование, списки, ссылки, отмена; цитата исходного письма хранится отдельно и уходит без изменений вёрстки; файлы, брошенные или вставленные в текст, становятся вложениями |
| Черновики | Автосохранение через 4 с после правки, сохранение при закрытии окна, продолжение из папки, перенос вложений, Bcc сохраняется |
| Отправители | Несколько отправителей с именами, Reply-To, скрытой копией и HTML-подписью; выбор в окне письма; адрес ограничен `identities.level` (single/domain/any) |
| Фильтры | ManageSieve: правила формой (условия по полям, заголовкам, тексту, размеру; переместить, копировать, переслать, отклонить, пометить, удалить), автоответ с датами, пересылка; формат правил Roundcube (`# rule:[…]`), правится активный скрипт; что нельзя показать формой — открывается как текст; защита от перезаписи чужих правок |
| Контакты | Адресная книга с группами, поиск по имени, адресу, телефону; «собранные адреса» — получатели отправленных писем; подсказки в полях адреса (группа раскрывается в участников); импорт и экспорт vCard и CSV (Outlook, Gmail, Thunderbird, в т.ч. Windows-1251); «В контакты» из карточки собеседника |
| Обновления | Новые письма во «Входящих» — push (IMAP IDLE → Server-Sent Events), остальные папки — опрос; непрочитанные в заголовке вкладки |
| Действия | Прочитано, флаг, перемещение, удаление в корзину / навсегда из корзины и спама |
| UI | Светлая и тёмная темы, бренд через CSS-переменные, адаптивная вёрстка, горячие клавиши |
| Безопасность | CSRF-токен + проверка Origin, CSP, письма в iframe без скриптов, аудит-лог |

Пока нет: картинки внутри текста письма, CardDAV,
смена пароля (в ядре только драйвер `none`), хранилища сессий sqlite/redis, 2FA.
План — в техническом задании.

За reverse proxy поток `/api/v1/events` должен проходить без буферизации
(для nginx сервер сам отдаёт `X-Accel-Buffering: no`; `proxy_read_timeout` — больше 25 с).

## Разработка

```sh
make devstand        # Postfix + Dovecot 2.4 на 127.0.0.1, ящики alice@/bob@test.local, пароль Test12345!
make run             # сервер с deploy/devstand/config.yaml
cd web && npm run dev  # Vite с проксированием /api на :8080
make test            # go vet, юнит-тесты, проверка типов фронтенда
make test-it         # интеграционные тесты против стенда
make e2e             # сквозной сценарий в headless Chromium (нужен playwright-core и chromium)
```

## Устройство

```
cmd/webmail/       main ядра: app.Main()
app/               публично: Main/Run(cfg, adapters...) — точка входа и для сборок с адаптерами
ext/               публично: интерфейсы точек расширения (semver)
internal/config    загрузка и проверка конфига
internal/session   сессии BFF
internal/mail      IMAP (go-imap v2), SMTP, MIME
internal/sanitize  очистка HTML писем
internal/sieve     клиент ManageSieve, разбор и генерация Sieve
internal/store     SQLite: отправители, подписи, контакты, группы, собранные адреса
internal/contacts  импорт и экспорт vCard / CSV
internal/api       REST API для SPA и реализация ext.Core
internal/web       встроенный SPA
web/               исходники SPA (Vue 3 + TypeScript)
deploy/devstand    тестовый почтовый стенд
```

Адаптер живёт в своём модуле и собирает свой бинарь:

```go
package main

import (
	"github.com/ibuben/squareball/app"
	"example.com/panel/webmailadapter"
)

func main() { app.Main(webmailadapter.New()) }
```

Включается секцией `adapters.<name>: {enabled: true, ...}` в конфиге.
