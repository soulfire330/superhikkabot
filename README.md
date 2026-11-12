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
| `tldr <текст>` или `tldr` реплаем | тезисный пересказ: 3–7 пунктов, вдвое короче исходника |
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

Доступ — только по белым спискам: `telegram.allowed_chats` (где бот вообще
подаёт голос) и `telegram.admin_users` (кому доступен `dev`, им же разрешена
личка). Всё, что приходит снаружи, молча игнорируется — включая `/start`.
Пустой `allowed_chats` означает «молчать везде».

## Прокси

```toml
[proxy]
llm = "socks5://127.0.0.1:1080"      # LLM и Firecrawl
digest = "http://127.0.0.1:3128"     # YouTube, GitHub и страницы для tldr
telegram = "http://127.0.0.1:3128"   # Bot API: поллинг, отправка, скачивание файлов
```

Поддержаны `http://`, `https://`, `socks5://` и `socks5h://` (в обоих socks-вариантах
имя хоста резолвит прокси). Пустое поле — прямое соединение. `digest` нужен, если
YouTube или другие сайты недоступны напрямую: без него `tldr <ссылка>` не скачает
страницу. Скачивание медиа ходит через `media.proxy` (`--proxy` для yt-dlp и
gallery-dl) — это отдельная настройка.

Ключи провайдера для агента (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, …) читаются из
того же окружения: `internal/harness` передаёт агенту только их, `PATH` и `HOME`,
а токен бота и прочие секреты ему не видны.

Веб-поиск у агента — расширение [`pi-firecrawl`](https://github.com/firecrawl/pi-firecrawl)
(`firecrawl_search`, `firecrawl_scrape`, `firecrawl_map`, …). Оно ставится в
`$HOME/.pi` (`HOME` юнита — `data/home`) и читает `FIRECRAWL_API_KEY` и
`FIRECRAWL_API_URL`; `internal/harness` передаёт их из `[firecrawl]` конфига, так
что агент ходит в тот же Firecrawl, что и бот.

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

`dev <задача>` запускает `pi --mode rpc` в рабочей копии и ждёт `agent_settled`.
**Каждый `dev` — новая pi-сессия**, а реплай на ответ агента продолжает именно её
(`switch_session`). Привязка «сообщение бота → сессия» лежит в `data/pi/replies.json`
и переживает перезапуск; всем чанкам длинного ответа соответствует одна сессия.
Дальше по цепочке:

1. агент правит код и коммитит;
2. бот гоняет `go vet ./...` и `go build -o bin/bot.new ./cmd/bot`;
3. при успехе: `bin/bot` → `bin/bot.prev`, новый бинарник на место, маркер `bin/boot.pending`, `syscall.Exec`.

Пока агент работает, сообщение-заглушка живёт: `internal/harness` отдаёт события
pi (`tool_execution_start/end`, `thinking_start`, `text_start`) через `ProgressFunc`,
а `internal/bot/status.go` перерисовывает её не чаще раза в 1,5 с — «🔧 running
<code>go test ./...</code>», «✏️ editing <code>internal/bot/status.go</code>»,
«💬 writing the answer…» — и по таймеру каждые 5 с показывает, сколько уже идёт
прогон.

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

## Ресурсы

Замеры на живом бинарнике: сам бинарник 22 МБ, RSS после инициализации пакетов
(trafilatura с wasm-re2) — **56 МБ**, один дайджест страницы добавляет ~10 МБ.

| Ресурс | Минимум | Комфорт | Почему |
|---|---|---|---|
| CPU | 1 | 2 | ffmpeg (кадры, склейка треков) и коллаж — короткие всплески; LLM и Telegram ждут сеть |
| RAM | 1 ГБ | 2 ГБ | 60 МБ база + `yt-dlp`/`ffmpeg` по 100–300 МБ + `pi` (Node) 200–400 МБ при `dev` |
| Диск | 12 ГБ | 20 ГБ | система ~2 ГБ, Go 300 МБ + кэш сборки 0.5–1.5 ГБ, `pi` ~600 МБ, venv `yt-dlp` ~150 МБ, ffmpeg ~150 МБ, данные бота ~1.5 ГБ |
| Swap | 1 ГБ | 1–2 ГБ | `go build` при самоправке — самый жирный разовый всплеск |

```
lxc.cgroup2.memory.max = 2G
lxc.cgroup2.memory.swap.max = 2G
lxc.cgroup2.cpu.max = 200000 100000
```

Что учесть:

- кэш сборки лучше держать на диске данных, иначе `go build` при самоправке
  съест гигабайт в `HOME` незаметно: `Environment=GOCACHE=/opt/superhikkabot/data/gocache`
  в юните;
- лимиты данных уже в конфиге: видео-артефакты 1 ГБ (уборка раз в 30 минут),
  фото-кэш 256 МБ (раз в 10 минут), медиа-временка удаляется после каждой загрузки;
- худший случай по памяти — альбом из 30 фото: `photo.Context` держит все байты в
  памяти перед сборкой коллажа. На 1 ГБ это впритык, лечится `photo.collage_max`;
- контейнер приватный и непривилегированный: нужны только systemd и сеть,
  ни `nesting`, ни docker.

## Проверки

```
make check     # gofmt, go vet, go test -race
```

## Лицензия

MIT, см. [LICENSE](LICENSE). Дайджест-экстракторы портированы из
[summario](https://github.com/wxtering/summario) (MIT, © 2026 wxtering) —
подробности в [NOTICE](NOTICE).
