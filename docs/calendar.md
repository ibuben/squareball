# Календарь: проект

Статус: черновик, ещё не реализовано. Сейчас кнопка календаря в шапке выключена (`web/src/components/MailView.vue`), а приглашения из писем только показываются и получают ответ (`internal/mail/invite.go`).

## Цель

Раздел «Календарь» в webmail, который работает с календарями на CalDAV-сервере: просмотр, создание, правка и удаление событий, сохранение приглашений из писем. Данные остаются на сервере, поэтому те же события видны в Thunderbird, iOS/macOS и DAVx⁵.

Не цель первой версии: свой сервер календарей, напоминания по почте, общий доступ к календарям, отправка приглашений участникам.

## Что уже есть

- **Radicale** на `127.0.0.1:5232`. webbeard-mail уже создаёт каждому ящику коллекцию `{ящик}/calendar/` (`VCALENDAR`, «Calendar»), а права `[calendars]` в `/etc/radicale/webbeard-rights` дают владельцу `rw` на `{user}/*`.
- **Адаптер CardDAV** (`adapters/carddav`): пресеты с `%u/%l/%d`, `password: session`, HTTP-клиент с защитой от SSRF, поиск коллекций через принципала. Календарь устроен так же.
- **Учётные данные** (`internal/api/books.go`, `davCreds`/`davSessionPassword`): пароль сессии, а при входе из панели — мастер-пароль для доверенного пресета на loopback.
- **Разбор iCalendar** (`internal/mail/invite.go`): часовые пояса (включая имена Outlook и VTIMEZONE), all-day, повторения через `rrule-go`. В `go.mod` уже есть `go-ical`, `go-webdav` (в нём есть `caldav.Client`) и `rrule-go`, новых зависимостей не нужно.

## Главное решение: без локального кэша

Контакты кэшируются в SQLite, потому что нужны для автодополнения адресов и поиска. Календарю это не нужно: интерфейс всегда показывает конкретный диапазон дат, а CalDAV умеет отдавать события за диапазон (`calendar-query` с `time-range`, RFC 4791 §9.9). Поэтому ядро ходит на сервер при каждом запросе диапазона и ничего не хранит.

Плюсы: нет синхронизации, ETag-ов в БД, миграций и устаревших данных; правки из других клиентов видны сразу. Минусы: каждый переход по неделям — запрос к серверу. Для локального Radicale это миллисекунды; для внешних серверов (этап 2) можно добавить кэш диапазонов в памяти на 1–2 минуты.

## Архитектура

```
CalendarView.vue ──/api/v1/calendar/*──▶ internal/api/calendar.go ──ext.CalendarSource──▶ adapters/caldav ──CalDAV──▶ Radicale
                                              │
                                              └─ internal/calendar (iCal ⇄ Event, разворот повторений)
```

### ext: интерфейс источника

```go
// CalendarPreset — календарь(и), которые администратор подключает всем ящикам.
type CalendarPreset struct {
	ID       string
	Name     string
	Account  ContactAccount // тот же тип: URL, логин, пароль, Trusted, PasswordSession
	ReadOnly bool
}

type RemoteCalendar struct {
	URL      string
	Name     string
	Color    string // calendar-color (Apple), если сервер отдаёт
	ReadOnly bool
}

type RemoteObject struct {
	Href string
	ETag string
	ICS  []byte // VCALENDAR как есть
}

type CalendarSource interface {
	CalendarPresets(mailbox string) []CalendarPreset
	DiscoverCalendars(ctx context.Context, acc ContactAccount) ([]RemoteCalendar, error)
	// Query — объекты, у которых есть вхождения в [from, to): сервер сам
	// учитывает повторения при отборе.
	Query(ctx context.Context, acc ContactAccount, cal string, from, to time.Time) ([]RemoteObject, error)
	GetObject(ctx context.Context, acc ContactAccount, cal, href string) (RemoteObject, error)
	// FindUID — объект с таким UID (для приглашений: обновить, а не задвоить).
	FindUID(ctx context.Context, acc ContactAccount, cal, uid string) (RemoteObject, error)
	PutObject(ctx context.Context, acc ContactAccount, cal, href, etag string, ics []byte) (newHref, newETag string, err error)
	DeleteObject(ctx context.Context, acc ContactAccount, cal, href, etag string) error
}
```

`ContactAccount` переиспользуем, чтобы не трогать существующий код. Его стоит переименовать в `DavAccount` (с алиасом для совместимости) отдельным коммитом. Ошибки — те же `ErrContactAuth/Conflict/ReadOnly/NotFound/Forbidden`, тоже с более общими именами-алиасами. Ядро регистрирует источник через `Core.SetCalendarSource`, по аналогии с `SetContactSource`.

### adapters/caldav

- Новый адаптер `caldav` на `github.com/emersion/go-webdav/caldav`.
- HTTP-клиент, `authClient`, `limitedBody`, SSRF-фильтр и `viaPrincipal` из `adapters/carddav` выносятся в общий пакет `adapters/internal/dav`, чтобы не копировать проверку безопасности.
- Конфиг повторяет CardDAV:

```yaml
adapters:
  caldav:
    enabled: true
    allow_user_accounts: false     # этап 2
    allow_private_networks: false
    presets:
      - id: mailbox
        name: Календарь
        url: "http://127.0.0.1:5232/%u/"   # принципал: найдутся все календари ящика
        username: "%u"
        password: session
```

### internal/calendar

Чистый пакет без сети, хорошо покрывается тестами:

- `Parse(ics) → []Master` и `Expand(master, from, to, loc) → []Occurrence` — разворот `RRULE/RDATE/EXDATE` и исключений с `RECURRENCE-ID`. Логику часовых поясов (`propTime`, VTIMEZONE, имена Outlook) переносим сюда из `internal/mail/invite.go`, а `invite.go` начинает её использовать.
- `Build(Event, original ics) → ics` — меняет только поля формы и сохраняет остальное (VALARM, X-свойства, ATTENDEE), как это делается с vCard у контактов.
- Ограничения: не больше 1000 вхождений на объект и 5000 на ответ, диапазон запроса не длиннее 1 года.

### API ядра

| Запрос | Назначение |
| --- | --- |
| `GET /api/v1/calendars` | календари ящика: `id`, имя, цвет, `readonly`, ошибка |
| `GET /api/v1/calendar/events?from=…&to=…&tz=Europe/Moscow` | вхождения в диапазоне по всем видимым календарям |
| `GET /api/v1/calendar/event?cal=…&href=…` | событие целиком для формы правки |
| `POST /api/v1/calendar/events` | создать или изменить; тело содержит `etag`, при несовпадении `409` |
| `POST /api/v1/calendar/events/delete` | удалить целиком или одно вхождение (`recurrence_id` → `EXDATE`) |
| `POST /api/v1/messages/invite-save` | сохранить приглашение из письма в календарь (по UID обновляет, а не дублирует) |

`id` календаря — непрозрачная строка (хеш пресета и URL), чтобы URL сервера не попадал в браузер. Учётные данные берутся той же функцией, что у книг (`davCreds` переименовать в общую); при входе из панели работает мастер-пароль на loopback, как в последнем коммите.

Вхождение в ответе:

```json
{"cal":"c1","href":"/info@c1.uz/calendar/abc.ics","etag":"\"…\"","uid":"abc",
 "recurrence_id":"20261201T090000Z","summary":"Планёрка","location":"",
 "start":"2026-12-01T12:00:00+03:00","end":"2026-12-01T12:30:00+03:00",
 "all_day":false,"recurring":true,"readonly":false}
```

Цвет и видимость календарей — пользовательские настройки (`prefs`, ключ `calendar`), поэтому миграция БД не нужна.

### Интерфейс

- `state.view` получает значение `'calendar'`, кнопка `CalendarDays` в шапке включается.
- `CalendarView.vue`: виды «Месяц», «Неделя», «День» и «Список» (повестка), переход «Сегодня», мини-календарь в левой панели со списком календарей и чекбоксами видимости. На узких экранах по умолчанию «Список».
- Сетка недели и дня — CSS grid с абсолютным позиционированием событий и раскладкой пересечений в колонки. Отдельную библиотеку не берём: FullCalendar тяжёлый и плохо подстраивается под тему.
- `EventDialog.vue`: название, весь день, начало и конец, часовой пояс (по умолчанию браузерный), повторение (нет / ежедневно / по будням / еженедельно / ежемесячно / ежегодно, «до даты» или «N раз»), место, описание, календарь. При правке повторяющегося события спрашиваем «Только это / Это и следующие / Все».
- `InviteCard.vue`: после «Принять» или «Под вопросом» событие сохраняется в календарь (настройка, по умолчанию включена), и показывается ссылка «Открыть в календаре». При «Отклонить» и `METHOD:CANCEL` событие удаляется из календаря, если оно там есть.
- Локали `ru.ts`/`en.ts`: раздел `calendar.*`. Первый день недели берётся из локали.

## Этапы

1. **MVP.** Пресет Radicale, просмотр месяц/неделя/список, создание, правка и удаление непериодических событий, показ периодических, сохранение приглашений. Документация в `docs/integration.md` и `deploy/config.example.yaml`.
2. **Повторения и календари.** Правка повторений (это / это и следующие / все), вид «День», несколько календарей с цветами, перетаскивание событий мышью, пользовательские CalDAV-учётки (таблицы `dav_accounts` получают поле `kind`), кэш диапазонов в памяти для внешних серверов.
3. **Совместная работа.** Приглашения участникам (`METHOD:REQUEST` через SMTP, ответы `REPLY` обновляют `PARTSTAT`), занятость (free-busy), напоминания в браузере через `VALARM` (пока открыта вкладка), API панели `calendars/stale`, если понадобится.

## Риски и проверки

- **Мастер-пароль в Radicale.** Нужно проверить, что `webbeard_radicale_auth` принимает мастер-пароль для `{user}/calendar/` так же, как для книг; иначе при входе из панели календарь будет недоступен.
- **Time-range в Radicale.** Radicale сам разворачивает повторения при отборе, но на больших календарях это медленно. Нужно замерить на календаре в ~5000 событий; при необходимости запрашивать шире и кэшировать.
- **Конфликты.** Правка из двух клиентов: `If-Match` с ETag, при `412` возвращаем `409` и предлагаем перезагрузить событие.
- **Часовые пояса.** Все расчёты ведутся на сервере в поясе, который присылает клиент (`tz`), а all-day события — датами без пояса. Нужны тесты на переход на летнее время и на «плавающие» события.
- **Безопасность.** Через пользовательские учётки нельзя достучаться до внутренних сетей (общий SSRF-фильтр). `DESCRIPTION` выводится как текст, без HTML. Размер ответа сервера ограничен.

## Тесты

- `internal/calendar`: разворот RRULE/EXDATE/RECURRENCE-ID, all-day, DST, сохранение неизвестных свойств при `Build`.
- `adapters/caldav`: против Radicale в CI, как уже сделано для CardDAV (`radicaleGet` в `internal/api/books_test.go`).
- `internal/api`: создание → выборка → правка с устаревшим ETag (`409`) → удаление; сохранение приглашения дважды не создаёт дубль.
- `web/e2e/smoke.mjs`: открыть календарь, создать событие, увидеть его в неделе.
