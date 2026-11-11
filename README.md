# superhikkabot

Telegram-бот для конференции с друзьями: отвечает на вопросы, делает дайджесты по
ссылкам, скачивает видео и фотопосты, понимает содержимое видео и фото — и умеет
править сам себя руками агента `pi`.

Без биллинга, без базы, без HTTP-сервера: один бинарник, long polling, systemd.

## Команды

| Ввод | Что делает |
|---|---|
| `open <вопрос>`, `ask <вопрос>` | ответ LLM; при нужде — веб-поиск Firecrawl со ссылками на источники |
| `tldr <ссылка>` | дайджест: YouTube (субтитры), GitHub (README, дерево, AGENTS.md), обычная страница (trafilatura, fallback на Firecrawl) |
| `dev <задача>` | агент `pi` в режиме RPC правит этот же репозиторий (только `telegram.admin_users`) |
| ссылка на TikTok / Instagram / YouTube Shorts | скачать видео или фотопост |
| реплай на сообщение бота | ответ с контекстом ветки |
| реплай на скачанное видео | распознанная речь (Groq Whisper) + 5 кадров в модель |
| реплай на фото | сами фото или коллаж с номерами ячеек |
| `/start` | приветствие |

В личке можно писать без команды и упоминания; в группах — только команда,
упоминание или реплай на сообщение бота.

## Конфиг

`config.toml` рядом с бинарником (путь можно задать через `CONFIG_PATH`).
Шаблон — [config.example.toml](config.example.toml). Секреты в файл не пишутся:

| Переменная | Зачем |
|---|---|
| `TELEGRAM_BOT_TOKEN` | токен бота, обязателен |
| `LLM_API_KEY` | ключ OpenAI-совместимого chat completions |
| `FIRECRAWL_API_KEY` | веб-поиск и fallback-экстракция страниц |
| `GROQ_API_KEY` | распознавание речи в видео |

Ключи провайдера для агента (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, …) читаются из
того же окружения: `internal/harness` передаёт агенту только их, `PATH` и `HOME`,
а токен бота и прочие секреты ему не видны.

## Установка в LXC

Что нужно на хосте-контейнере:

```
apt install golang ffmpeg python3-venv git ripgrep
python3 -m venv /opt/media && /opt/media/bin/pip install "yt-dlp[default,curl-cffi]" gallery-dl
npm install -g @earendil-works/pi-coding-agent
```

`yt-dlp` и `gallery-dl` ищутся в `PATH` (или задаются в `media.ytdlp` / `media.gallery_dl`).
Если их нет, бот не падает: медиа-ссылка вернёт ошибку в чат, всё остальное работает.

```
git clone <repo> /opt/superhikkabot && cd /opt/superhikkabot
go build -o bin/bot ./cmd/bot
cp config.example.toml config.toml   # правим чаты, админов, адрес LLM
cp deploy/env.example /etc/superhikkabot.env && chmod 600 /etc/superhikkabot.env
cp deploy/superhikkabot.service /etc/systemd/system/
systemctl enable --now superhikkabot
```

Важно: `WorkingDirectory` юнита — тот же каталог, который правит агент, а `PATH`
юнита должен содержать `go`, `git`, `pi`, `ffmpeg`, `yt-dlp`, `gallery-dl`.

В группе с выключенным Privacy Mode бот видит все сообщения; с включённым — только
команды и реплаи, и тогда упоминания без `/` до него не доходят.

## Самоправка

`dev <задача>` запускает `pi --mode rpc` в рабочей копии и ждёт `agent_settled`:

1. агент правит код и коммитит;
2. бот гоняет `go vet ./...` и `go build -o bin/bot.new ./cmd/bot`;
3. при успехе: `bin/bot` → `bin/bot.prev`, новый бинарник на место, маркер `bin/boot.pending`, `syscall.Exec`.

Если `go vet` или сборка не прошли — бот остаётся на старом бинарнике и пишет причину
в чат. Если новый бинарник упал до рабочего состояния, при следующем старте
`harness.CheckBoot` уводит процесс на `bin/bot.prev` и снимает маркер. Для этого у
юнита должен быть `Restart=always`.

`harness.deploy = false` отключает шаг сборки: агент просто правит код, перезапуск
остаётся на человеке.

## Что живёт только в памяти

Reply-ветки (TTL 100 минут), привязка артефактов видео и фото к `message_id`,
очередь загрузок. После перезапуска контекст начинается заново; сами файлы
(аудио, кадры, кэш фото) лежат в `data/` до истечения TTL.

## Проверки

```
make check     # gofmt, go vet, go test -race
```
