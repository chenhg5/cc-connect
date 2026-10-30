# Руководство по настройке Telegram

[English](telegram.md) | Русский

Это руководство поможет подключить **cc-connect** к Telegram, чтобы общаться с локальным Claude Code через Telegram-бота.

## Предварительные требования

- Учётная запись Telegram
- Компьютер, на котором можно запустить cc-connect (публичный IP-адрес не нужен)
- Установленный и настроенный Claude Code

> 💡 **Преимущество**: используется режим Long Polling — публичный IP-адрес, домен и обратный прокси не нужны.

---

## Шаг 1. Создайте Telegram-бота

### 1.1 Откройте BotFather

Найдите в Telegram **@BotFather** (официальный бот для управления ботами) и начните диалог.

> ⚠️ Убедитесь, что это официальный BotFather с отметкой верификации. Не используйте сторонние подделки.

### 1.2 Создайте нового бота

Отправьте команду `/newbot`. BotFather попросит указать имя и имя пользователя.

### 1.3 Задайте имя бота

Введите **отображаемое имя** бота (например, `cc-connect`).

### 1.4 Задайте имя пользователя бота

Введите **имя пользователя** (оно должно заканчиваться на `bot`, например `cc_connect_bot`).

> 💡 **Правила для имени пользователя:**
> - Должно заканчиваться на `bot` (регистр не учитывается)
> - Допускаются только буквы, цифры и символы подчёркивания
> - Должно быть уникальным во всём Telegram

### 1.5 Получите токен бота

После создания бота BotFather ответит примерно так:

```
Done! Congratulations on your new bot...
Use this token to access the HTTP API:
1234567890:ABCdefGHIjklMNOpqrsTUVwxyz-123456

Keep your token secure...
```

> ⚠️ Сразу сохраните токен и никому его не раскрывайте — он показывается только один раз! Если потеряете его, создайте новый: `/mybots` → выберите бота → `API Token` (токен API) → `Revoke current token` (отозвать текущий токен).

---

## Шаг 2. Настройте cc-connect

Добавьте токен в `config.toml`:

```toml
[[projects]]
name = "my-project"

# ── Project-level settings ──────────────────────────────────
# admin_from: who can run privileged commands (/shell, /restart, /upgrade).
#   Not set (default) → privileged commands are blocked for everyone.
#   "*" → all allowed users get admin access (only for personal single-user setups).
#   "id1,id2" → only these Telegram user IDs can run privileged commands.
admin_from = "*"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"
mode = "default"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "1234567890:ABCdefGHIjklMNOpqrsTUVwxyz-123456"

# ── Platform-level settings ─────────────────────────────────
# allow_from: who can use this bot.
#   Not set (default) → all users are permitted (a WARN will be logged).
#   "*" → same as not set, but explicit (no WARN).
#   "id1,id2" → only these Telegram user IDs can interact with the bot.
# allow_from = "123456789"

# Optional proxy for Telegram Bot API access.
# Supports URLs like:
#   http://127.0.0.1:7890
#   socks5://127.0.0.1:1080
# proxy = "http://127.0.0.1:7890"
# proxy_username = ""
# proxy_password = ""
```

> **Частая ошибка:** `admin_from` нужно указывать в `[[projects]]` (на уровне проекта), а НЕ в `[projects.platforms.options]`. В неправильном разделе параметр будет проигнорирован без предупреждения. `admin_from` определяет, кто может выполнять привилегированные команды, а `allow_from` — кто вообще может пользоваться ботом.
>
> Чтобы узнать свой ID пользователя Telegram, отправьте любое сообщение боту **@userinfobot**.

### 2.1 Необязательно: используйте прокси

Если компьютер не может напрямую подключиться к Telegram Bot API, укажите прямой прокси в параметрах платформы Telegram:

```toml
[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "1234567890:ABCdefGHIjklMNOpqrsTUVwxyz-123456"
proxy = "socks5://127.0.0.1:1080"
proxy_username = ""
proxy_password = ""
```

Примечания:

- `proxy` принимает URL прокси HTTP или SOCKS5.
- Оставьте `proxy_username` и `proxy_password` пустыми, если прокси не требует аутентификации.
- Прокси влияет только на запросы к Telegram Bot API для этого экземпляра платформы Telegram.

---

## Шаг 3. Получите ID чата (необязательно)

Если вы хотите ограничить использование бота определёнными пользователями или группами, вам понадобится ID чата (Chat ID).

### 3.1 Получите ID личного чата

1. Отправьте любое сообщение своему боту
2. Откройте следующий URL (замените `{{TOKEN}}` своим токеном):

```
https://api.telegram.org/bot{{TOKEN}}/getUpdates
```

3. Найдите поле `chat.id` в полученном JSON

### 3.2 Получите ID группового чата

1. Добавьте бота в группу
2. Отправьте в группу сообщение с упоминанием @your_bot
3. Проверьте ответ по URL `getUpdates` — ID групповых чатов обычно отрицательные

> Примечание: поддержка списка разрешённых ID чатов запланирована на будущий выпуск.

---

## Шаг 4. Настройте команды бота (необязательно)

### 4.1 Настройте меню команд

Отправьте BotFather:

```
/setcommands
```

Выберите своего бота, затем введите список команд:

```
help - Show available commands
new - Start a new session
list - List sessions
```

### 4.2 Задайте описание бота

```
/setdescription
```

Введите описание — пользователи увидят его, когда впервые откроют чат с ботом.

---

## Шаг 5. Запустите cc-connect

### 5.1 Запуск

```bash
cc-connect
# Or specify a config file
cc-connect -config /path/to/config.toml
```

### 5.2 Проверьте подключение

В журнале должны появиться примерно такие записи:

```
level=INFO msg="telegram: connected" bot=cc_connect_bot
level=INFO msg="platform started" project=my-project platform=telegram
level=INFO msg="cc-connect is running" projects=1
```

---

## Шаг 6. Начните общение

### 6.1 Личные сообщения

1. Найдите бота в Telegram по имени пользователя
2. Нажмите «Start» (начать), чтобы запустить диалог
3. Отправьте сообщение

### 6.2 Групповой чат

1. Создайте или откройте группу
2. Перейдите в настройки группы → «Add members» (добавить участников)
3. Найдите и добавьте своего бота
4. Отправляйте сообщения в группу

### 6.3 Сессии в темах

Темы Telegram содержат `message_thread_id`. cc-connect включает этот идентификатор
в ключ сессии Telegram, поэтому у каждой темы свой независимый
контекст разговора. Это относится к темам форумов в группах и темам личных чатов,
когда Telegram передаёт `message_thread_id`.

---

## Пример использования

```
User: Help me analyze the current project structure

cc-connect: 🤔 Thinking...
cc-connect: 🔧 Tool: Bash(ls -la)
cc-connect: Here's the project structure...
```

---

## Архитектура

```
┌─────────────────────────────────────────────────────────────┐
│                      Telegram Cloud                          │
│                                                              │
│   User Message ──→ Telegram Bot API ◄── Long Polling         │
│                          ▲                                   │
└──────────────────────────┼───────────────────────────────────┘
                           │
                           │ HTTPS (no public IP needed)
                           ▼
┌─────────────────────────────────────────────────────────────┐
│                    Your Local Machine                         │
│                                                              │
│   cc-connect ◄──► Claude Code CLI ◄──► Your Project Code    │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## Сравнение Long Polling и Webhook

| Характеристика | Long Polling | Webhook |
|---------|-------------|---------|
| Публичный IP-адрес | ❌ Не нужен | ✅ Нужен |
| Домен | ❌ Не нужен | ✅ Нужен |
| Сертификат HTTPS | ❌ Не нужен | ✅ Нужен |
| Сложность | Просто | Сложнее |
| Задержка | Низкая (долгий опрос) | Низкая |
| Лучше подходит для | Локальной разработки, частной сети | Рабочей среды (production) |

---

## Частые вопросы

### Бот не отвечает на сообщения?

Проверьте следующее:

1. Запущен ли cc-connect?
2. Правильно ли указан токен бота?
3. Отправляли ли вы сообщение после запуска cc-connect? (Бот получает только сообщения, отправленные после запуска.)

### Как создать новый токен?

1. Отправьте BotFather команду `/mybots`
2. Выберите своего бота
3. Нажмите `API Token` (токен API) → `Revoke current token` (отозвать текущий токен)

### Бот не отвечает в группах?

Убедитесь, что режим конфиденциальности в группах отключён. В BotFather: `/mybots` → выберите бота → `Bot Settings` (настройки бота) → `Group Privacy` (конфиденциальность в группах) → `Turn off` (отключить).

---

## Справочные материалы

- [Документация Telegram Bot API](https://core.telegram.org/bots/api)
- [Руководство по BotFather](https://core.telegram.org/bots#botfather)
- [Учебное руководство по Telegram-ботам](https://core.telegram.org/bots/tutorial)

---

## См. также

- [Настройка Feishu](./feishu.md)
- [Настройка DingTalk](./dingtalk.md)
- [Настройка Weibo](./weibo.md)
- [Настройка Slack](./slack.md)
- [Настройка Discord](./discord.ru.md)
- [Вернуться к README](../README.md)
