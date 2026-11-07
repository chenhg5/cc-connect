# Спецификация протокола платформы Bridge

[English](bridge-protocol.md) | [中文](bridge-protocol.zh-CN.md) | [Русский](bridge-protocol.ru.md)

> Версия: 1.0-draft\
> Статус: черновик — возможны изменения до реализации

## Обзор

Протокол Bridge позволяет **внешним адаптерам платформ**, написанным на любом языке программирования, подключаться к cc-connect во время работы через WebSocket. Благодаря этому для интеграции каждой новой платформы не требуется писать код на Go и заново компилировать исполняемый файл.

### Архитектура

```
┌──────────────────────────────────────────────────────┐
│                    cc-connect                        │
│                                                      │
│   ┌────────────┐ ┌────────────┐ ┌────────────────┐  │
│   │  Telegram   │ │   Feishu   │ │ BridgePlatform │  │
│   │  (native)   │ │  (native)  │ │  (WebSocket)   │  │
│   └─────┬──────┘ └─────┬──────┘ └───────┬────────┘  │
│         │              │                │            │
│         └──────────────┴────────────────┘            │
│                        │                             │
│                  ┌─────┴─────┐                       │
│                  │   Engine   │                       │
│                  └───────────┘                       │
└──────────────────────────────────────────────────────┘
                         │ WebSocket
              ┌──────────┴───────────┐
              │                      │
   ┌──────────┴──────┐  ┌───────────┴─────┐
   │  Python Adapter  │  │ Node.js Adapter  │
   │ (WeChat, Line…)  │  │ (Custom Chat…)   │
   └─────────────────┘  └─────────────────┘
```

`BridgePlatform` — встроенная в cc-connect платформа, которая:

1. Предоставляет конечную точку WebSocket для подключения внешних адаптеров.
2. Преобразует сообщения WebSocket в вызовы интерфейса `core.Platform`.
3. Направляет ответы движка обратно адаптеру через то же соединение WebSocket.

---

## Подключение

### Конечная точка

```
ws://<host>:<port>/bridge/ws
```

Порт и путь задаются в `config.toml`:

```toml
[bridge]
enabled = true
port = 9810
path = "/bridge/ws"       # optional, default "/bridge/ws"
token = "your-secret"     # required for authentication
```

### Аутентификация

При подключении адаптер должен пройти аутентификацию одним из следующих способов:

| Способ | Пример |
|--------|---------|
| Параметр строки запроса | `ws://host:9810/bridge/ws?token=your-secret` |
| Заголовок HTTP | `Authorization: Bearer your-secret` |
| Заголовок HTTP | `X-Bridge-Token: your-secret` |

Подключения без успешной аутентификации отклоняются со статусом ответа HTTP 401.

### Жизненный цикл соединения

```
Adapter                          cc-connect
  │                                  │
  │──── WebSocket Connect ──────────→│  (with token)
  │                                  │
  │──── register ──────────────────→│  (declare platform name & capabilities)
  │←─── register_ack ──────────────│  (confirm or reject)
  │                                  │
  │←──→ message / reply exchange ──→│  (bidirectional)
  │                                  │
  │──── ping ──────────────────────→│  (keepalive, every 30s recommended)
  │←─── pong ──────────────────────│
  │                                  │
  │──── close ─────────────────────→│  (graceful disconnect)
```

---

## Протокол обмена сообщениями

Все сообщения представляют собой объекты JSON с обязательным полем `type`. Протокол использует JSON с разделением переводами строк, передаваемый в текстовых кадрах WebSocket (один объект JSON на кадр).

### Адаптер → cc-connect

#### `register`

Должно быть первым сообщением после подключения. Объявляет идентификационные данные адаптера и поддерживаемые им возможности.

```json
{
  "type": "register",
  "platform": "wechat",
  "capabilities": ["text", "image", "file", "audio", "card", "buttons", "typing", "update_message", "preview"],
  "metadata": {
    "version": "1.0.0",
    "description": "WeChat Official Account adapter"
  }
}
```

**Поля:**

| Поле | Тип | Обязательное | Описание |
|-------|------|----------|-------------|
| `type` | string | да | `"register"` |
| `platform` | string | да | Уникальное имя платформы (строчные буквы, цифры и дефисы). Используется в ключах сессий. |
| `capabilities` | string[] | да | Список поддерживаемых возможностей (см. [Возможности](#возможности)). |
| `metadata` | object | нет | Метаданные произвольного формата для журналирования и отладки. |

#### `message`

Передаёт входящее сообщение пользователя движку.

```json
{
  "type": "message",
  "msg_id": "msg-001",
  "session_key": "wechat:user123:user123",
  "user_id": "user123",
  "user_name": "Alice",
  "content": "Hello, what can you do?",
  "reply_ctx": "conv-abc-123",
  "images": [],
  "files": [],
  "audio": null
}
```

**Поля:**

| Поле | Тип | Обязательное | Описание |
|-------|------|----------|-------------|
| `type` | string | да | `"message"` |
| `msg_id` | string | да | Идентификатор сообщения на платформе для трассировки. |
| `session_key` | string | да | Уникальный идентификатор сессии. Формат: `{platform}:{scope}:{user}`. Правила его формирования определяет адаптер. |
| `user_id` | string | да | Идентификатор пользователя на платформе. |
| `user_name` | string | нет | Отображаемое имя. |
| `content` | string | да | Текстовое содержимое. |
| `reply_ctx` | string | да | Строка контекста, которую cc-connect не интерпретирует и которая нужна адаптеру для маршрутизации ответов. cc-connect возвращает её без изменений в каждом ответе. |
| `images` | Image[] | нет | Прикреплённые изображения (см. [Объект изображения](#объект-изображения)). |
| `files` | File[] | нет | Прикреплённые файлы (см. [Объект файла](#объект-файла)). |
| `audio` | Audio | нет | Голосовое сообщение (см. [Объект аудио](#объект-аудио)). |

#### `card_action`

Пользователь нажал кнопку или выбрал вариант на карточке.

```json
{
  "type": "card_action",
  "session_key": "wechat:user123:user123",
  "action": "cmd:/new",
  "reply_ctx": "conv-abc-123"
}
```

**Поля:**

| Поле | Тип | Обязательное | Описание |
|-------|------|----------|-------------|
| `type` | string | да | `"card_action"` |
| `session_key` | string | да | Сессия, в которой было выполнено действие. |
| `action` | string | да | Значение обратного вызова кнопки (например, `"cmd:/new"`, `"nav:/model"`, `"act:/heartbeat pause"`). |
| `reply_ctx` | string | да | Контекст ответа для его маршрутизации. |

#### `preview_ack`

Подтверждает начало предварительного отображения и возвращает дескриптор для последующих обновлений.

```json
{
  "type": "preview_ack",
  "ref_id": "preview-req-001",
  "preview_handle": "platform-msg-id-789"
}
```

#### `ping`

Поддерживает соединение активным. cc-connect отвечает сообщением `pong`.

```json
{
  "type": "ping",
  "ts": 1710000000000
}
```

---

### cc-connect → Адаптер

#### `register_ack`

Подтверждает или отклоняет регистрацию.

```json
{
  "type": "register_ack",
  "ok": true,
  "error": ""
}
```

#### `reply`

Полное сообщение с ответом для отправки пользователю.

```json
{
  "type": "reply",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "content": "I can help you with coding tasks!",
  "format": "text"
}
```

**Поля:**

| Поле | Тип | Обязательное | Описание |
|-------|------|----------|-------------|
| `type` | string | да | `"reply"` |
| `session_key` | string | да | Целевая сессия. |
| `reply_ctx` | string | да | Значение из исходного сообщения, возвращаемое без изменений. |
| `content` | string | да | Текст ответа. |
| `format` | string | нет | `"text"` (по умолчанию) или `"markdown"`. |

#### `reply_stream`

Очередной фрагмент потокового ответа для предварительного отображения набираемого текста в реальном времени. Отправляется только в том случае, если адаптер объявил поддержку возможности `"preview"`.

```json
{
  "type": "reply_stream",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "delta": "partial content...",
  "full_text": "accumulated full text so far...",
  "preview_handle": "platform-msg-id-789",
  "done": false
}
```

| Поле | Тип | Описание |
|-------|------|-------------|
| `delta` | string | Новый текст, появившийся после предыдущего сообщения потока. |
| `full_text` | string | Весь накопленный текст. Адаптеры могут использовать его для обновления с полной заменой содержимого сообщения. |
| `preview_handle` | string | Дескриптор, возвращённый в `preview_ack`. В первом сообщении потока пуст. |
| `done` | bool | Значение `true` в последнем сообщении потока. |

#### `preview_start`

Запрашивает у адаптера создание начального сообщения для предварительного отображения потокового ответа.

```json
{
  "type": "preview_start",
  "ref_id": "preview-req-001",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "content": "Thinking..."
}
```

Адаптер должен отправить сообщение и ответить сообщением `preview_ack`, содержащим идентификатор сообщения на платформе.

#### `update_message`

Запрашивает у адаптера редактирование существующего сообщения на месте. Используется для обновления предварительно отображаемого потокового ответа.

```json
{
  "type": "update_message",
  "session_key": "wechat:user123:user123",
  "preview_handle": "platform-msg-id-789",
  "content": "Updated text content..."
}
```

#### `delete_message`

Запрашивает у адаптера удаление сообщения (например, для удаления сообщений предварительного отображения).

```json
{
  "type": "delete_message",
  "session_key": "wechat:user123:user123",
  "preview_handle": "platform-msg-id-789"
}
```

#### `card`

Отправляет пользователю структурированную карточку. Отправляется только в том случае, если адаптер объявил поддержку возможности `"card"`; иначе cc-connect использует `reply` с содержимым, полученным через `card.RenderText()`.

```json
{
  "type": "card",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "card": {
    "header": {
      "title": "Model Selection",
      "color": "blue"
    },
    "elements": [
      {
        "type": "markdown",
        "content": "Choose a model:"
      },
      {
        "type": "actions",
        "buttons": [
          {"text": "GPT-4", "btn_type": "primary", "value": "cmd:/model switch gpt-4"},
          {"text": "Claude", "btn_type": "default", "value": "cmd:/model switch claude"}
        ],
        "layout": "row"
      },
      {
        "type": "divider"
      },
      {
        "type": "note",
        "text": "Current: gpt-4"
      }
    ]
  }
}
```

Полный справочник элементов карточки см. в разделе [Схема карточки](#схема-карточки).

#### `buttons`

Отправляет сообщение со встроенными кнопками. Отправляется только в том случае, если адаптер объявил поддержку возможности `"buttons"`.

```json
{
  "type": "buttons",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "content": "Allow tool execution: bash(rm -rf /tmp/old)?",
  "buttons": [
    [
      {"text": "✅ Allow", "data": "perm:req-123:allow"},
      {"text": "❌ Deny", "data": "perm:req-123:deny"}
    ]
  ]
}
```

`buttons` — двумерный массив: каждый вложенный массив задаёт один ряд.

#### `typing_start`

Запрашивает у адаптера отображение индикатора набора текста.

```json
{
  "type": "typing_start",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123"
}
```

#### `typing_stop`

Запрашивает у адаптера скрытие индикатора набора текста.

```json
{
  "type": "typing_stop",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123"
}
```

#### `audio`

Отправляет голосовое или аудиосообщение. Отправляется только в том случае, если адаптер объявил поддержку возможности `"audio"`.

```json
{
  "type": "audio",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "data": "<base64-encoded-audio>",
  "format": "mp3"
}
```

#### `image`

Отправляет пользователю изображение. Отправляется только в том случае, если адаптер объявил поддержку возможности `"image"`.

```json
{
  "type": "image",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "data": "<base64-encoded-image>",
  "mime_type": "image/png",
  "file_name": "screenshot.png"
}
```

#### `file`

Отправляет пользователю файл. Отправляется только в том случае, если адаптер объявил поддержку возможности `"file"`.

```json
{
  "type": "file",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "data": "<base64-encoded-file>",
  "mime_type": "application/pdf",
  "file_name": "report.pdf"
}
```

#### `pong`

Ответ на `ping`.

```json
{
  "type": "pong",
  "ts": 1710000000000
}
```

#### `error`

Уведомляет адаптер об ошибке на стороне сервера.

```json
{
  "type": "error",
  "code": "session_not_found",
  "message": "No active session for the given key"
}
```

---

## Схемы данных

### Возможности

| Возможность | Описание | Что включает |
|------------|-------------|---------|
| `text` | Базовый обмен текстовыми сообщениями (обязательно) | `message`, `reply` |
| `image` | Отправка и получение изображений | `message.images`, ответ `image` |
| `file` | Отправка и получение файлов | `message.files`, ответ `file` |
| `audio` | Отправка и получение голосовых сообщений | `message.audio`, ответ `audio` |
| `card` | Отображение структурированных карточек с расширенным оформлением | Ответ `card` |
| `buttons` | Встроенные интерактивные кнопки | Ответ `buttons`, `card_action` |
| `typing` | Индикатор набора текста | `typing_start`, `typing_stop` |
| `update_message` | Редактирование существующих сообщений | `update_message` |
| `preview` | Предварительное отображение потокового ответа (требует `update_message`) | `preview_start`, `reply_stream` |
| `delete_message` | Удаление сообщений | `delete_message` |
| `reconstruct_reply` | Восстановление контекста ответа из session_key | Позволяет отправлять сообщения cron/heartbeat |

Если поддержка возможности не объявлена, cc-connect автоматически переходит к упрощённому поведению:

- Нет `card` → карточки преобразуются в обычный текст через `RenderText()`.
- Нет `buttons` → кнопки пропускаются или отображаются как текстовые подсказки.
- Нет `preview` → потоковая передача отключается; отправляется только итоговый ответ.
- Нет `typing` → индикаторы набора текста не отображаются.

### Объект изображения

```json
{
  "mime_type": "image/png",
  "data": "<base64-encoded>",
  "file_name": "screenshot.png"
}
```

### Объект файла

```json
{
  "mime_type": "application/pdf",
  "data": "<base64-encoded>",
  "file_name": "report.pdf"
}
```

### Объект аудио

```json
{
  "mime_type": "audio/ogg",
  "data": "<base64-encoded>",
  "format": "ogg",
  "duration": 5
}
```

### Схема карточки

Карточка состоит из необязательного заголовка и списка элементов:

```json
{
  "header": {
    "title": "Card Title",
    "color": "blue"
  },
  "elements": [ ... ]
}
```

**Поддерживаемые цвета:** `blue`, `green`, `red`, `orange`, `purple`, `grey`, `turquoise`, `violet`, `indigo`, `wathet`, `yellow`, `carmine`.

#### Типы элементов

**Markdown**
```json
{"type": "markdown", "content": "**Bold** and _italic_"}
```

**Разделитель**
```json
{"type": "divider"}
```

**Действия (ряд кнопок)**
```json
{
  "type": "actions",
  "buttons": [
    {"text": "Click Me", "btn_type": "primary", "value": "cmd:/do-something"}
  ],
  "layout": "row"
}
```

`btn_type`: `"primary"`, `"default"`, `"danger"`.\
`layout`: `"row"` (по умолчанию), `"equal_columns"`.

**Элемент списка (описание + кнопка)**
```json
{
  "type": "list_item",
  "text": "GPT-4 — Most capable model",
  "btn_text": "Select",
  "btn_type": "primary",
  "btn_value": "cmd:/model switch gpt-4"
}
```

**Выбор (раскрывающийся список)**
```json
{
  "type": "select",
  "placeholder": "Choose a model",
  "options": [
    {"text": "GPT-4", "value": "cmd:/model switch gpt-4"},
    {"text": "Claude", "value": "cmd:/model switch claude"}
  ],
  "init_value": "cmd:/model switch gpt-4"
}
```

**Примечание (сноска)**
```json
{
  "type": "note",
  "text": "Tip: use /help to see all commands",
  "tag": "optional-machine-tag"
}
```

---

## Формат ключа сессии

Ключи сессий имеют следующий формат:

```
{platform}:{scope}:{user_id}
```

- **platform**: имя `platform`, указанное при регистрации (например, `wechat`).
- **scope**: область группировки — идентификатор группы или канала либо то же значение, что и `user_id`, для личных чатов один на один.
- **user_id**: уникальный идентификатор пользователя.

Примеры:

- `wechat:user123:user123` — личная переписка
- `wechat:group456:user123` — пользователь в групповом чате
- `matrix:room789:alice` — комната Matrix

За единообразное формирование ключей сессий отвечает адаптер.

---

## REST API для управления сессиями

Помимо протокола WebSocket для обмена сообщениями в реальном времени, сервер Bridge предоставляет на том же порту конечные точки HTTP REST для управления сессиями. Это позволяет адаптерам получать список сессий, создавать, переключать и удалять их без отдельного API управления (Management API).

### Аутентификация

Для конечных точек REST используется тот же токен, что и для соединений WebSocket:

| Способ | Пример |
|--------|---------|
| Заголовок HTTP | `Authorization: Bearer your-secret` |
| Параметр строки запроса | `?token=your-secret` |

### Формат ответа

Все ответы используют ту же внешнюю структуру, что и API управления:

```json
{"ok": true, "data": { ... }}
{"ok": false, "error": "message"}
```

### Конечные точки

Пути всех конечных точек указаны относительно базового URL сервера Bridge (например, `http://localhost:9810`).

#### GET /bridge/sessions

Возвращает список сессий для заданного префикса ключа сессии (обычно `platform:chatId`).

**Параметры строки запроса:**

| Параметр | Тип | Обязательный | Описание |
|-------|------|----------|-------------|
| `session_key` | string | да | Ключ сессии, для которого нужно получить список сессий (например, `wechat:user123:user123`). |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "sessions": [
      {
        "id": "s1",
        "name": "default",
        "history_count": 12
      },
      {
        "id": "s2",
        "name": "work",
        "history_count": 5
      }
    ],
    "active_session_id": "s1"
  }
}
```

---

#### POST /bridge/sessions

Создаёт новую именованную сессию.

**Тело запроса:**

```json
{
  "session_key": "wechat:user123:user123",
  "name": "work"
}
```

| Поле | Тип | Обязательное | Описание |
|-------|------|----------|-------------|
| `session_key` | string | да | Ключ сессии пользователя. |
| `name` | string | нет | Понятное человеку имя сессии. По умолчанию — `"default"`. |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "id": "s3",
    "name": "work",
    "message": "session created"
  }
}
```

---

#### GET /bridge/sessions/{id}

Возвращает подробные сведения о сессии вместе с историей сообщений.

**Параметры строки запроса:**

| Параметр | Тип | По умолчанию | Описание |
|-------|------|---------|-------------|
| `session_key` | string | (обязательный) | Ключ сессии для определения контекста проекта. |
| `history_limit` | int | 50 | Максимальное число возвращаемых записей истории. |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "id": "s1",
    "name": "default",
    "history": [
      {"role": "user", "content": "Hello"},
      {"role": "assistant", "content": "Hi! How can I help?"}
    ]
  }
}
```

---

#### DELETE /bridge/sessions/{id}

Удаляет сессию и её историю.

**Параметры строки запроса:**

| Параметр | Тип | Обязательный | Описание |
|-------|------|----------|-------------|
| `session_key` | string | да | Ключ сессии для определения контекста проекта. |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "message": "session deleted"
  }
}
```

---

#### POST /bridge/sessions/switch

Переключает активную сессию для указанного ключа сессии.

**Тело запроса:**

```json
{
  "session_key": "wechat:user123:user123",
  "target": "s2"
}
```

| Поле | Тип | Обязательное | Описание |
|-------|------|----------|-------------|
| `session_key` | string | да | Ключ сессии. |
| `target` | string | да | Идентификатор или имя сессии, на которую нужно переключиться. |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "message": "session switched",
    "active_session_id": "s2"
  }
}
```

---

## Обработка ошибок

### Повторное подключение

При разрыве соединения WebSocket адаптер должен:

1. Выждать паузу с экспоненциальным увеличением задержки (начиная с 1 секунды, максимум — 60 секунд).
2. Подключиться повторно и отправить новое сообщение `register`.
3. Возобновить обычную работу — cc-connect поддерживает состояние сессий независимо от соединения.

### Порядок сообщений

В рамках одного соединения WebSocket порядок сообщений сохраняется. cc-connect последовательно обрабатывает сообщения адаптера для каждого ключа сессии.

### Тайм-ауты

- **Интервал Ping**: адаптеры должны отправлять `ping` не реже одного раза в 30 секунд.
- **Тайм-аут соединения**: cc-connect закрывает неактивные соединения, если в течение 90 секунд не поступает ping.
- **Тайм-аут ответа**: если агент отвечает слишком долго, cc-connect может отправить ответ с ошибкой. Специальная обработка на стороне адаптера не требуется.

---

## Пример конфигурации

```toml
[bridge]
enabled = true
port = 9810
token = "a-strong-random-secret"

# Optional: restrict which adapters can connect (by platform name).
# Default: allow all registered adapters.
# allow_platforms = ["wechat", "matrix"]
```

Отдельная конфигурация проекта для каждого адаптера не требуется — адаптеры связываются с **проектом по умолчанию** либо указывают поле `project` в сообщении `register` для привязки к конкретному проекту.

---

## Рекомендации по разработке SDK

При разработке адаптера придерживайтесь следующих рекомендаций:

1. **Не храните состояние в адаптере** — адаптер должен быть тонким слоем преобразования. Всё состояние сессий хранится в cc-connect.
2. **Обрабатывайте повторные подключения** — сетевые сбои неизбежны. Реализуйте экспоненциальное увеличение задержки между попытками.
3. **Достоверно объявляйте возможности** — объявляйте только те возможности, которые ваша платформа действительно поддерживает.
4. **Передавайте `reply_ctx` без изменений** — всегда возвращайте исходное значение `reply_ctx` из первоначального сообщения.
5. **Используйте Base64 для двоичных данных** — изображения, файлы и аудио передаются в виде строк в кодировке base64.
6. **Записывайте ошибки в журнал, не завершайте работу аварийно** — при получении неизвестного типа сообщения запишите его в журнал и продолжайте работу.

### Минимальный пример адаптера (псевдокод на Python)

```python
import asyncio
import json
import websockets

async def main():
    uri = "ws://localhost:9810/bridge/ws?token=your-secret"
    async with websockets.connect(uri) as ws:
        # 1. Register
        await ws.send(json.dumps({
            "type": "register",
            "platform": "my-chat",
            "capabilities": ["text", "buttons"]
        }))
        ack = json.loads(await ws.recv())
        assert ack["ok"], f"Registration failed: {ack['error']}"

        # 2. Start message loop
        async def recv_loop():
            async for raw in ws:
                msg = json.loads(raw)
                if msg["type"] == "reply":
                    send_to_chat_platform(msg["reply_ctx"], msg["content"])
                elif msg["type"] == "buttons":
                    send_buttons_to_chat(msg["reply_ctx"], msg["content"], msg["buttons"])
                # ... handle other types

        async def send_loop():
            while True:
                chat_msg = await get_next_chat_message()
                await ws.send(json.dumps({
                    "type": "message",
                    "msg_id": chat_msg.id,
                    "session_key": f"my-chat:{chat_msg.user_id}:{chat_msg.user_id}",
                    "user_id": chat_msg.user_id,
                    "user_name": chat_msg.user_name,
                    "content": chat_msg.text,
                    "reply_ctx": chat_msg.conversation_id
                }))

        await asyncio.gather(recv_loop(), send_loop())

asyncio.run(main())
```

---

## Управление версиями

Версия протокола объявляется в сообщении `register` через `metadata.protocol_version`. Текущая версия — `1`. cc-connect отклоняет подключения с несовместимыми версиями и отвечает сообщением `register_ack`, содержащим ошибку.

```json
{
  "type": "register",
  "platform": "my-chat",
  "capabilities": ["text"],
  "metadata": {
    "protocol_version": 1
  }
}
```
