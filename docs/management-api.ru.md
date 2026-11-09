# Спецификация API управления cc-connect

[English](management-api.md) | [中文](management-api.zh-CN.md) | [Русский](management-api.ru.md)

> **Версия:** 1.1-draft\
> **Статус:** черновик — возможны изменения до реализации\
> **Последнее обновление:** 2026-03-24

---

## 1. Обзор

API управления cc-connect — это REST API на основе HTTP, который позволяет внешним приложениям (веб-панелям управления, клиентам с текстовым интерфейсом, настольным приложениям с графическим интерфейсом, приложениям в строке меню Mac) управлять экземплярами cc-connect и отслеживать их состояние. Он дополняет существующий внутренний API на основе Unix-сокетов, предоставляя доступный по сети интерфейс с аутентификацией по токену, подходящий для средств как удалённого, так и локального управления.

### 1.1 Архитектура

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         cc-connect Process                               │
│                                                                          │
│  ┌──────────────────┐    ┌──────────────────┐    ┌──────────────────┐  │
│  │  Unix Socket API │    │  Management API   │    │  Bridge Server   │  │
│  │  (internal)      │    │  (HTTP :9820)     │    │  (WebSocket)     │  │
│  └────────┬─────────┘    └────────┬─────────┘    └────────┬─────────┘  │
│           │                       │                       │             │
│           └───────────────────────┼───────────────────────┘             │
│                                   │                                      │
│                          ┌────────┴────────┐                            │
│                          │  Core Engine(s)  │                            │
│                          │  Projects       │                            │
│                          │  Sessions      │                            │
│                          │  Cron/Heartbeat │                            │
│                          └─────────────────┘                            │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
              ┌─────────────────────┼─────────────────────┐
              │                     │                     │
     ┌────────┴────────┐   ┌───────┴───────┐   ┌─────────┴─────────┐
     │  Web Dashboard  │   │  TUI Client   │   │  Mac Tray App      │
     └─────────────────┘   └───────────────┘   └────────────────────┘
```

### 1.2 Принципы проектирования

- **RESTful:** URL, ориентированные на ресурсы, стандартные методы HTTP
- **JSON:** Все тела запросов и ответов используют `application/json`
- **Единая структура ответов:** Каждый ответ имеет вид `{"ok": true|false, "data"|"error": ...}`
- **Аутентификация по токену:** Токен Bearer или параметр строки запроса для всех конечных точек

---

## 2. Конфигурация

### 2.1 Блок настроек управления

Добавьте в `config.toml` следующее:

```toml
[management]
enabled = true
port = 9820
token = "mgmt-secret"
```

| Поле | Тип | По умолчанию | Описание |
|----------|---------|-----------|--------------------------------------------------|
| `enabled`| boolean | `false` | Включение сервера API управления |
| `port` | integer | `9820` | TCP-порт, на котором сервер принимает соединения |
| `token` | string | (обязательно) | Общий секрет для аутентификации |

Если `enabled` имеет значение `false`, API управления не запускается. Токен должен быть стойкой случайной строкой (например, длиной не менее 32 символов).

### 2.2 Базовый URL

Пути всех конечных точек задаются относительно:

```
http://<host>:<port>/api/v1
```

Пример: `http://localhost:9820/api/v1/status`

---

## 3. Аутентификация

Каждый запрос должен содержать действительный токен. Поддерживаются два способа:

### 3.1 Токен Bearer (рекомендуется)

```
Authorization: Bearer <token>
```

Пример:

```bash
curl -H "Authorization: Bearer mgmt-secret" http://localhost:9820/api/v1/status
```

### 3.2 Параметр строки запроса

```
GET /api/v1/status?token=mgmt-secret
```

> **Примечание:** Аутентификация через параметр строки запроса предусмотрена для сред, в которых сложно задавать заголовки HTTP. Из соображений безопасности предпочтителен токен Bearer (токены в URL могут попасть в журналы).

### 3.3 Ответ при ошибке аутентификации

Если токен отсутствует или недействителен:

- **Статус ответа HTTP:** `401 Unauthorized`
- **Тело ответа:**

```json
{
  "ok": false,
  "error": "unauthorized: missing or invalid token"
}
```

---

## 4. Формат ответа

### 4.1 Успешный ответ

```json
{
  "ok": true,
  "data": { ... }
}
```

### 4.2 Ошибка

```json
{
  "ok": false,
  "error": "human-readable error message"
}
```

### 4.3 Коды статуса ответа HTTP

| Код | Значение |
|------|----------------------------------------------|
| 200 | Успешное выполнение |
| 400 | Некорректный запрос (неверное тело запроса, отсутствующие параметры) |
| 401 | Ошибка аутентификации (отсутствующий или недействительный токен) |
| 404 | Ресурс не найден (проект, сессия и т. д.) |
| 405 | Метод не разрешён |
| 500 | Внутренняя ошибка сервера |

---

## 5. Справочник конечных точек

### 5.1 Система

#### GET /api/v1/status

Возвращает состояние системы и сводные сведения.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "version": "v1.2.0",
    "uptime_seconds": 3600,
    "connected_platforms": ["feishu", "telegram"],
    "projects_count": 2,
    "bridge_adapters": [
      {
        "platform": "custom",
        "project": "my-backend",
        "capabilities": ["text", "images"]
      }
    ]
  }
}
```

| Поле | Тип | Описание |
|-----------------------|----------|--------------------------------------------------|
| `version` | string | Версия cc-connect (например, `v1.2.0`) |
| `uptime_seconds` | number | Время работы процесса в секундах |
| `connected_platforms` | string[] | Типы платформ, подключённых в данный момент |
| `projects_count` | number | Количество настроенных проектов |
| `bridge_adapters` | array | Внешние адаптеры, подключённые через WebSocket Bridge |

---

#### POST /api/v1/restart

Инициирует перезапуск с корректным завершением работы. Процесс корректно завершит текущую работу и повторно запустит себя через exec. В сессию, из которой был инициирован перезапуск, может быть отправлено сообщение об успешном перезапуске (если применимо).

**Тело запроса (необязательно):**

```json
{
  "session_key": "telegram:123:456",
  "platform": "telegram"
}
```

Если тело запроса передано, после запуска нового процесса в указанную сессию будет отправлено уведомление о перезапуске.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "message": "restart initiated"
  }
}
```

---

#### POST /api/v1/reload

Повторно загружает конфигурацию с диска без перезапуска процесса. Могут быть добавлены новые проекты; удалённые из конфигурации проекты останавливаются. Изменённые настройки проектов вступают в силу.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "message": "config reloaded",
    "projects_added": ["new-project"],
    "projects_removed": [],
    "projects_updated": ["my-backend"]
  }
}
```

---

#### GET /api/v1/config

Возвращает текущую конфигурацию со скрытыми секретами. Полезно для отладки и отображения в интерфейсе.

**Параметры строки запроса:** отсутствуют

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "data_dir": "/home/user/.cc-connect",
    "language": "en",
    "projects": [
      {
        "name": "my-backend",
        "agent": {
          "type": "claudecode",
          "providers": [
            {
              "name": "anthropic",
              "api_key": "***",
              "base_url": "",
              "model": "claude-sonnet-4-20250514"
            }
          ]
        },
        "platforms": [
          {
            "type": "feishu",
            "options": {
              "app_id": "***",
              "app_secret": "***"
            }
          }
        ]
      }
    ]
  }
}
```

Секреты (например, `api_key`, `token`, `app_secret`, `client_secret`) заменяются на `"***"`.

---

#### GET /api/v1/logs

Возвращает последние записи журнала.

**Параметры строки запроса:**

| Параметр | Тип | По умолчанию | Описание |
|---------|--------|---------|--------------------------------------|
| `level` | string | `info` | Минимальный уровень: `debug`, `info`, `warn`, `error` |
| `limit` | int | `100` | Максимальное количество возвращаемых записей (1–1000) |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "entries": [
      {
        "time": "2026-03-10T10:30:00Z",
        "level": "info",
        "message": "api server started",
        "attrs": {"socket": "/home/user/.cc-connect/run/api.sock"}
      }
    ]
  }
}
```

---

### 5.2 Проекты

#### GET /api/v1/projects

Возвращает список всех проектов с краткими сведениями.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "projects": [
      {
        "name": "my-backend",
        "agent_type": "claudecode",
        "platforms": ["feishu", "telegram"],
        "sessions_count": 3,
        "heartbeat_enabled": true
      }
    ]
  }
}
```

---

#### GET /api/v1/projects/{name}

Возвращает подробные сведения об одном проекте.

**Параметры пути:**

| Параметр | Тип | Описание |
|--------|--------|--------------------|
| `name` | string | Имя проекта |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "name": "my-backend",
    "agent_type": "claudecode",
    "platforms": [
      {
        "type": "feishu",
        "connected": true
      },
      {
        "type": "telegram",
        "connected": true
      }
    ],
    "sessions_count": 3,
    "active_session_keys": ["telegram:123:456", "feishu:ou_xxx:chat_xxx"],
    "heartbeat": {
      "enabled": true,
      "paused": false,
      "interval_mins": 30,
      "session_key": "telegram:123:456"
    },
    "settings": {
      "quiet": false,
      "admin_from": "user1,user2",
      "language": "en",
      "disabled_commands": ["restart", "upgrade"]
    }
  }
}
```

**Ошибка (404):**

```json
{
  "ok": false,
  "error": "project not found: my-backend"
}
```

---

#### PATCH /api/v1/projects/{name}

Обновляет настройки проекта. Изменяются только переданные поля.

**Тело запроса:**

```json
{
  "quiet": true,
  "admin_from": "user1,user2,user3",
  "language": "zh",
  "disabled_commands": ["restart", "upgrade", "cron"]
}
```

| Поле | Тип | Описание |
|---------------------|----------|----------------------------------------------------------|
| `quiet` | boolean | Скрытие сообщений о ходе рассуждений и работы инструментов |
| `admin_from` | string | Идентификаторы пользователей, которым доступны привилегированные команды, через запятую; `"*"` = все |
| `language` | string | Язык интерфейса: `en`, `zh`, `zh-TW`, `ja`, `es` |
| `disabled_commands` | string[] | Команды, которые нужно отключить (например, `restart`, `upgrade`, `cron`) |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "name": "my-backend",
    "settings": {
      "quiet": true,
      "admin_from": "user1,user2,user3",
      "language": "zh",
      "disabled_commands": ["restart", "upgrade", "cron"]
    }
  }
}
```

---

### 5.3 Сессии

Сессии — это контексты диалогов внутри проекта. Сессия идентифицируется по `session_key` (формат: `platform:chatId:userId`), а именованные сессии — также по необязательному внутреннему `id` (например, `/new work` создаёт именованную сессию).

#### GET /api/v1/projects/{name}/sessions

Возвращает список сессий проекта с краткими сведениями, включая фрагмент последнего сообщения.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "sessions": [
      {
        "id": "sess_abc123",
        "session_key": "telegram:123:456",
        "name": "work",
        "platform": "telegram",
        "agent_type": "claudecode",
        "active": true,
        "live": true,
        "history_count": 12,
        "created_at": "2026-03-10T09:00:00Z",
        "updated_at": "2026-03-10T10:30:00Z",
        "last_message": {
          "role": "assistant",
          "content": "Done! The tests are passing now...",
          "timestamp": "2026-03-10T10:30:00Z"
        },
        "user_name": "Alice",
        "chat_name": "dev-channel"
      }
    ],
    "active_keys": {
      "telegram:123:456": "telegram"
    }
  }
}
```

| Поле | Тип | Описание |
|----------------|---------|-------------------------------------------------------------------|
| `active` | boolean | Выбрана ли эта сессия для соответствующего ключа пользователя |
| `live` | boolean | Есть ли у этой сессии работающий процесс агента |
| `last_message` | object | Краткие сведения о последнем сообщении (роль, содержимое, обрезанное до 200 символов, временная метка). `null`, если история отсутствует. |
| `user_name` | string | Отображаемое имя пользователя (из метаданных платформы) |
| `chat_name` | string | Название чата или канала (из метаданных платформы) |
| `active_keys` | object | Соответствие ключей сессий с активными подключениями агента названиям платформ: ключ сессии → название платформы |

---

#### POST /api/v1/projects/{name}/sessions

Создаёт новую сессию.

**Тело запроса:**

```json
{
  "session_key": "telegram:123:456",
  "name": "work"
}
```

| Поле | Тип | Обязательно | Описание |
|--------------|--------|----------|--------------------------------------|
| `session_key`| string | да | Ключ маршрутизации платформы (например, `telegram:123:456`) |
| `name` | string | нет | Понятное пользователю имя сессии |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "id": "sess_xyz789",
    "session_key": "telegram:123:456",
    "name": "work",
    "created_at": "2026-03-10T10:35:00Z"
  }
}
```

---

#### GET /api/v1/projects/{name}/sessions/{id}

Возвращает подробные сведения о сессии, включая историю сообщений.

**Параметры пути:**

| Параметр | Тип | Описание |
|--------|--------|--------------------------------------|
| `name` | string | Имя проекта |
| `id` | string | Идентификатор сессии |

**Параметры строки запроса:**

| Параметр | Тип | По умолчанию | Описание |
|---------|------|---------|--------------------------------|
| `history_limit` | int | 50 | Максимальное количество возвращаемых записей истории |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "id": "sess_abc123",
    "session_key": "telegram:123:456",
    "name": "work",
    "platform": "telegram",
    "agent_type": "claudecode",
    "agent_session_id": "as_xxx",
    "active": true,
    "live": true,
    "history_count": 12,
    "created_at": "2026-03-10T09:00:00Z",
    "updated_at": "2026-03-10T10:30:00Z",
    "history": [
      {
        "role": "user",
        "content": "Hello",
        "timestamp": "2026-03-10T09:00:05Z"
      },
      {
        "role": "assistant",
        "content": "Hi! How can I help?",
        "timestamp": "2026-03-10T09:00:10Z"
      }
    ]
  }
}
```

| Поле | Тип | Описание |
|---------|---------|---------------------------------------------------------------|
| `live` | boolean | Есть ли у сессии активный процесс агента (может получать сообщения через `/send`) |

---

#### DELETE /api/v1/projects/{name}/sessions/{id}

Удаляет сессию и её историю.

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

#### POST /api/v1/projects/{name}/sessions/switch

Переключает активную сессию для заданного session_key (например, когда у пользователя несколько именованных сессий).

**Тело запроса:**

```json
{
  "session_key": "telegram:123:456",
  "session_id": "sess_xyz789"
}
```

| Поле | Тип | Обязательно | Описание |
|---------------|--------|----------|--------------------------------|
| `session_key` | string | да | Ключ маршрутизации платформы |
| `session_id` | string | да | Идентификатор сессии, которую нужно сделать активной |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "message": "active session switched",
    "active_session_id": "sess_xyz789"
  }
}
```

---

#### POST /api/v1/projects/{name}/send

Отправляет сообщение в сессию. Сообщение доставляется агенту так, как если бы пользователь отправил его через платформу. **У сессии должен быть работающий процесс агента**. Перед отправкой убедитесь в этом, проверив поле `live` в подробных сведениях о сессии.

**Тело запроса:**

```json
{
  "session_key": "telegram:123:456",
  "message": "Review the latest commit"
}
```

| Поле | Тип | Обязательно | Описание |
|---------------|--------|----------|--------------------------------|
| `session_key`| string | да | Ключ маршрутизации платформы |
| `message` | string | да | Текст для отправки агенту |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "message": "message sent"
  }
}
```

---

### 5.4 Провайдеры

Провайдеры — это серверные службы API (например, Anthropic, OpenAI или пользовательские конечные точки), которые предоставляют модель ИИ агенту проекта.

#### GET /api/v1/projects/{name}/providers

Возвращает список провайдеров с указанием активного.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "providers": [
      {
        "name": "anthropic",
        "active": true,
        "model": "claude-sonnet-4-20250514",
        "base_url": ""
      },
      {
        "name": "relay",
        "active": false,
        "model": "claude-sonnet-4-20250514",
        "base_url": "https://api.relay.example.com"
      }
    ],
    "active_provider": "anthropic"
  }
}
```

---

#### POST /api/v1/projects/{name}/providers

Добавляет нового провайдера.

**Тело запроса:**

```json
{
  "name": "relay",
  "api_key": "sk-xxx",
  "base_url": "https://api.relay.example.com",
  "model": "claude-sonnet-4-20250514",
  "thinking": "disabled",
  "env": {
    "CLAUDE_CODE_USE_BEDROCK": "1",
    "AWS_PROFILE": "bedrock"
  }
}
```

| Поле | Тип | Обязательно | Описание |
|-----------|-----------------|----------|------------------------------------------|
| `name` | string | да | Идентификатор провайдера |
| `api_key` | string | нет* | Ключ API (*обязателен, если не задано `env`) |
| `base_url`| string | нет | Пользовательская конечная точка API |
| `model` | string | нет | Переопределение модели |
| `thinking`| string | нет | `"disabled"` для провайдеров без поддержки адаптивных рассуждений |
| `env` | object (ключ/значение) | нет | Дополнительные переменные окружения |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "name": "relay",
    "message": "provider added"
  }
}
```

---

#### DELETE /api/v1/projects/{name}/providers/{provider}

Удаляет провайдера. Активного провайдера удалить нельзя; сначала переключитесь на другого.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "message": "provider removed"
  }
}
```

**Ошибка (400):**

```json
{
  "ok": false,
  "error": "cannot remove active provider; switch to another first"
}
```

---

#### POST /api/v1/projects/{name}/providers/{provider}/activate

Переключает активного провайдера.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "active_provider": "relay",
    "message": "provider activated"
  }
}
```

---

#### GET /api/v1/projects/{name}/models

Возвращает список моделей, доступных для типа агента проекта.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "models": [
      "claude-sonnet-4-20250514",
      "claude-3-5-sonnet-20241022",
      "claude-3-opus-20240229"
    ],
    "current": "claude-sonnet-4-20250514"
  }
}
```

---

#### POST /api/v1/projects/{name}/model

Задаёт модель для проекта.

**Тело запроса:**

```json
{
  "model": "claude-3-5-sonnet-20241022"
}
```

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "model": "claude-3-5-sonnet-20241022",
    "message": "model updated"
  }
}
```

---

### 5.5 Задания cron

#### GET /api/v1/cron

Возвращает список всех заданий cron с возможностью фильтрации по проекту.

**Параметры строки запроса:**

| Параметр | Тип | Описание |
|-----------|--------|--------------------|
| `project` | string | Фильтр по проекту |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "jobs": [
      {
        "id": "cron_abc123",
        "project": "my-backend",
        "session_key": "telegram:123:456",
        "cron_expr": "0 6 * * *",
        "prompt": "Summarize GitHub trending",
        "exec": "",
        "work_dir": "",
        "description": "Daily GitHub Trending",
        "enabled": true,
        "silent": true,
        "created_at": "2026-03-10T08:00:00Z",
        "last_run": "2026-03-10T06:00:00Z",
        "last_error": ""
      }
    ]
  }
}
```

---

#### POST /api/v1/cron

Добавляет задание cron. Необходимо передать либо `prompt`, либо `exec`, но не оба поля одновременно.

**Тело запроса (задание с промптом):**

```json
{
  "project": "my-backend",
  "session_key": "telegram:123:456",
  "cron_expr": "0 6 * * *",
  "prompt": "Summarize GitHub trending",
  "description": "Daily GitHub Trending",
  "silent": true
}
```

**Тело запроса (задание с командой оболочки):**

```json
{
  "project": "my-backend",
  "session_key": "telegram:123:456",
  "cron_expr": "0 9 * * 1",
  "exec": "npm run weekly-report",
  "work_dir": "/path/to/project",
  "description": "Weekly Report",
  "silent": false
}
```

| Поле | Тип | Обязательно | Описание |
|--------------|---------|----------|------------------------------------------------|
| `project` | string | нет* | Имя проекта (*обязательно, если проектов несколько) |
| `session_key`| string | да | Целевая сессия для заданий с промптом |
| `cron_expr` | string | да | Выражение cron (5 или 6 полей) |
| `prompt` | string | нет* | Промпт для отправки (*обязателен, если не задано `exec`) |
| `exec` | string | нет* | Команда оболочки (*обязательна, если не задано `prompt`) |
| `work_dir` | string | нет | Рабочий каталог для выполнения команды |
| `description`| string | нет | Понятное пользователю описание |
| `silent` | boolean | нет | Отключение уведомления о запуске |
| `session_mode` | string | нет | `reuse` (по умолчанию) или `new_per_run` — новая сессия агента при каждом запуске |
| `timeout_mins` | int | нет | Время ожидания планировщика для одного запуска: если не задано — 30 минут, `0` — без ограничения времени |

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "id": "cron_xyz789",
    "project": "my-backend",
    "session_key": "telegram:123:456",
    "cron_expr": "0 6 * * *",
    "prompt": "Summarize GitHub trending",
    "description": "Daily GitHub Trending",
    "enabled": true,
    "created_at": "2026-03-10T10:40:00Z"
  }
}
```

---

#### DELETE /api/v1/cron/{id}

Удаляет задание cron.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "message": "cron job deleted"
  }
}
```

#### POST /api/v1/cron/{id}/exec

Немедленно запускает существующее задание cron. Отключённые задания по-прежнему можно запускать вручную.
`/api/v1/cron/{id}/run` также принимается как альтернативный путь для совместимости.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "id": "cron_xyz789",
    "status": "triggered"
  }
}
```

---

### 5.6 Периодические запросы (Heartbeat)

Механизм Heartbeat периодически выполняет промпты в сессии (например, «проверь входящие»), чтобы агент оставался в курсе состояния окружения.

#### GET /api/v1/projects/{name}/heartbeat

Возвращает состояние механизма Heartbeat.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "enabled": true,
    "paused": false,
    "interval_mins": 30,
    "only_when_idle": true,
    "session_key": "telegram:123:456",
    "silent": true,
    "run_count": 42,
    "error_count": 0,
    "skipped_busy": 5,
    "last_run": "2026-03-10T10:00:00Z",
    "last_error": ""
  }
}
```

---

#### POST /api/v1/projects/{name}/heartbeat/pause

Приостанавливает работу механизма Heartbeat.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "message": "heartbeat paused"
  }
}
```

---

#### POST /api/v1/projects/{name}/heartbeat/resume

Возобновляет работу механизма Heartbeat.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "message": "heartbeat resumed"
  }
}
```

---

#### POST /api/v1/projects/{name}/heartbeat/run

Немедленно запускает однократное выполнение Heartbeat.

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "message": "heartbeat triggered"
  }
}
```

---

#### POST /api/v1/projects/{name}/heartbeat/interval

Задаёт интервал выполнения Heartbeat.

**Тело запроса:**

```json
{
  "minutes": 15
}
```

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "interval_mins": 15,
    "message": "interval updated"
  }
}
```

---

### 5.7 Bridge

#### GET /api/v1/bridge/adapters

Возвращает список подключённых адаптеров Bridge (внешние платформы, подключённые через WebSocket).

**Ответ:**

```json
{
  "ok": true,
  "data": {
    "adapters": [
      {
        "platform": "custom",
        "project": "my-backend",
        "capabilities": ["text", "images", "files"],
        "connected_at": "2026-03-10T09:00:00Z"
      }
    ]
  }
}
```

---

## 6. Правила обработки ошибок

### 6.1 Стандартный ответ об ошибке

Все ответы об ошибках имеют одинаковую структуру:

```json
{
  "ok": false,
  "error": "human-readable message"
}
```

### 6.2 Типичные ошибки

| HTTP | Пример сообщения об ошибке | Причина |
|------|-------------------------------------------------|--------------------------------|
| 400 | `"project is required (multiple projects)"` | Отсутствует обязательный параметр |
| 400 | `"either prompt or exec is required"` | Некорректное тело запроса для задания cron |
| 401 | `"unauthorized: missing or invalid token"` | Ошибка аутентификации |
| 404 | `"project not found: xyz"` | Неизвестный проект, сессия или задание cron |
| 404 | `"session not found"` | Неизвестный идентификатор сессии |
| 405 | `"method not allowed"` | Неверный метод HTTP |
| 500 | `"internal error"` | Непредвиденная ошибка сервера |

### 6.3 Ошибки валидации

Если тело запроса не прошло валидацию:

```json
{
  "ok": false,
  "error": "invalid request: session_key is required"
}
```

---

## 7. Формат ключа сессии

`session_key` — составной идентификатор, который используется для маршрутизации сообщений на нужную платформу и в нужный чат:

```
<platform>:<chat_id>:<user_id>
```

Примеры:

- `telegram:123456789:123456789` — пользователь Telegram 123456789 в чате 123456789
- `feishu:ou_xxx:chat_yyy` — пользователь Feishu в чате
- `slack:C01234:U05678` — канал и пользователь Slack
- `discord:123456789:987654321` — сервер и пользователь Discord

В режиме нескольких рабочих пространств формат может включать префикс рабочего пространства:

```
<workspace>:<platform>:<chat_id>:<user_id>
```

---

## 8. CORS

При использовании API управления веб-панелями должна быть предусмотрена настройка заголовков CORS. Предлагаемое расширение конфигурации:

```toml
[management]
enabled = true
port = 9820
token = "mgmt-secret"
cors_origins = ["http://localhost:3000", "https://dashboard.example.com"]
```

Если настройка отсутствует, CORS может быть отключён или использовать значение по умолчанию (например, `*` только для запросов с того же источника).

---

## 9. История изменений

| Версия | Дата | Изменения |
|-----------|------------|----------------------------|
| 1.1-draft | 2026-03-24 | В список сессий и подробные сведения о сессии добавлены поля `live`, `last_message`, `agent_type`, `user_name`, `chat_name`, `active_keys` |
| 1.0-draft | 2026-03-10 | Первоначальная спецификация |

---

## 10. Ссылки

- [Протокол Bridge](bridge-protocol.md) — протокол WebSocket для адаптеров внешних платформ
- [Руководство пользователя](usage.md) — возможности для конечных пользователей и слеш-команды
- [config.example.toml](../config.example.toml) — шаблон конфигурации
