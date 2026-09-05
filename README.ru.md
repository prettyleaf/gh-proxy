# gh-proxy

[EN](README.md) | RU

Форк [hunshcn/gh-proxy](https://github.com/hunshcn/gh-proxy) на Go, дополненный новыми возможностями под личные предпочтения и задачи других контрибьюторов и/или единомышленников. Монтируется на путь, а не на корень домена, поэтому живёт рядом с уже работающим сайтом; не выдаёт своего существования — всё неавторизованное это обычный `404`; и по умолчанию работает только по токену.

```
https://sub.example.com/ivanghproxy/ТОКЕН/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz
└──────── ваш сайт ────┘└─ префикс ─┘└───┘ └──────────────── обычный GitHub-URL ─────────────────────────┘
                                    токен
```

## Что умеет

* релизы, архивы веток и тегов, `blob`/`raw`, gist;
* `git clone` и `fetch` (git smart HTTP) — без настройки git;
  `push` тоже проходит, но только если задан `GHP_UPSTREAM_TOKEN` с правом записи: собственные креденшелы клиента до GitHub не доезжают, их вырезают;
* докачка и параллельная загрузка (`Range` проходит насквозь);
* серверное следование редиректам на CDN-бэкенды GitHub;
* приватные репозитории через собственный PAT (`GHP_UPSTREAM_TOKEN`);
* ограничение по владельцам/репозиториям (allow/deny-листы);
* опциональная [короткая форма URL](#короткая-форма) — без `https://github.com` в ссылке;
* опциональная [страница статуса](#страница-статуса) со счётчиками, графиком по минутам и метриками для Prometheus.

## Быстрый старт

```bash
git clone https://github.com/prettyleaf/gh-proxy && cd gh-proxy

cp .env.example .env
openssl rand -hex 24 # задайте GHP_PREFIX в .env

docker compose up -d
```

## Reverse-proxy

```nginx
location = /ivanghproxy {
    return 404;
  }

location /ivanghproxy/ {
    proxy_pass http://127.0.0.1:8899;

    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;

    proxy_buffering         off;
    proxy_request_buffering off;
    client_max_body_size    0;
    proxy_read_timeout      1h;
    proxy_send_timeout      1h;

    proxy_redirect off;
    add_header X-Robots-Tag "noindex, nofollow, noarchive" always;
    access_log off;
}
```

Проверка:

```bash
BASE='https://sub.example.com/ivanghproxy/ВАШ_ТОКЕН'

curl -LO "$BASE/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz"
git clone "$BASE/https://github.com/cli/browser"

curl -s -o /dev/null -w '%{http_code}\n' https://sub.example.com/ivanghproxy/   # 404
```

Подробнее — [docs/nginx.md](docs/nginx.md).

## Документация

| | |
|---|---|
| [docs/nginx.md](docs/nginx.md) | монтирование на путь: рабочий конфиг, разбор каждой директивы, проверка, диагностика, Caddy/Traefik/Cloudflare |
| [docs/clients.md](docs/clients.md) | git, curl, wget, aria2, `insteadOf`, CI, Dockerfile |
| [docs/security.md](docs/security.md) | модель угроз: стелс-404, утечки токена, границы SSRF, что не уезжает на GitHub |

## Как передаётся токен

Четыре равнозначных способа:

```bash
# сегмент пути — основной, единственный работающий с git из коробки
curl "$BASE/https://raw.githubusercontent.com/cli/cli/trunk/README.md"

# заголовки — для curl/wget/CI
curl -H "Authorization: Bearer ТОКЕН"  "https://sub.example.com/ivanghproxy/https://..."
curl -u "x:ТОКЕН"                      "https://sub.example.com/ivanghproxy/https://..."
curl -H "X-Proxy-Token: ТОКЕН"         "https://sub.example.com/ivanghproxy/https://..."
```

Токен в пути нужен потому, что прокси отвечает `404`, а не `401`: git делает первый запрос без креденшелов и ждёт `401 WWW-Authenticate`, чтобы понять, что надо авторизоваться. Стелс-404 такого вызова не шлёт — а токен, уже вписанный в URL, делает обмен ненужным.

## Короткая форма

`GHP_DEFAULT_HOST` разрешает не писать хост: точка монтирования начинает
изображать сам GitHub, и ссылка — это обычный GitHub-URL с отрезанным хостом.

```bash
GHP_DEFAULT_HOST=github.com,raw.githubusercontent.com
```

```
https://github.com/prettyleaf/media/blob/main/logo.png
https://sub.example.com/ivanghproxy/ТОКЕН/prettyleaf/media/blob/main/logo.png   # тот же файл
```

Хосты перебираются по порядку: сначала формы `github.com` (`blob`/`raw`, релизы,
архивы, теги, git), затем `raw.githubusercontent.com` — поэтому голый
`/owner/repo/ref/path` тоже работает:

```bash
curl -O "$BASE/prettyleaf/media/main/logo.png"          # -> raw.githubusercontent.com
curl -LO "$BASE/cli/cli/releases/download/v1/gh.tar.gz" # -> github.com
git clone "$BASE/cli/browser"
```

Принимаются только те хосты, с которыми прокси и так разговаривает; любой другой —
ошибка старта. URL, в котором хост указан, сохраняет свой смысл: он никогда не
перечитывается как имя владельца.

По умолчанию пусто, и включать стоит только если нужно именно зеркало: тогда
любой путь `/owner/repo/...` под точкой монтирования уезжает на GitHub, а значит
при `GHP_PREFIX=/` на домене, где живёт что-то ещё, прокси затенит настоящие
пути. Префикс и токен продолжают гейтить доступ.

## Страница статуса

Одна самодостаточная страница — без внешних ресурсов, без базы, счётчики живут в
памяти и обнуляются при рестарте: запросы и отданные байты, отказы по причинам,
цели по типам URL, топ репозиториев, последние 25 запросов, график за последний
час и та конфигурация, с которой процесс реально запущен. Токена на ней нет.

```bash
GHP_STATUS_PATH=/ghp-status    # выключена, пока это не задано
```

После этого страница отвечает на `https://sub.example.com/ghp-status`, рядом с
ней — `/ghp-status/json` (её и опрашивает сама страница, раз в 5 с) и
`/ghp-status/metrics` (текстовый формат Prometheus).

Путь задаётся от корня сайта, а не внутри `GHP_PREFIX`, — чтобы у reverse-proxy
был ровно один `location`, который можно закрыть:

| `GHP_STATUS_AUTH` | |
|---|---|
| `token` (по умолчанию) | тот же токен, что и у прокси: [любым из четырёх способов](#как-передаётся-токен) плюс `?token=...`, чтобы открыть в браузере |
| `none` | прокси не проверяет ничего — для случая, когда этот `location` уже закрыт tinyauth, basic-аутентификацией или SSO forward-auth. Конфиг — в [docs/nginx.md](docs/nginx.md) |

Пока переменная не задана, всё под этим путём отвечает тем же `404`, что и
остальной сервис; так же отвечает неверный токен и неизвестный подпуть.

На админском listener то же самое доступно всегда — `/status`, `/status/json` и
`/metrics`: он слушает loopback, так что добраться до него = уже быть на хосте.

```bash
curl -s 127.0.0.1:8900/metrics
```

Запросы к самой странице в счётчики не попадают: иначе на графике не было бы
ничего, кроме её собственного опроса.

## Вариант без токена

`GHP_ALLOW_ANONYMOUS=1` полностью выключает аутентификацию. `GHP_TOKEN` при этом
должен быть пустым: заданы оба — ошибка старта, не задано ни одного — тоже,
прокси откажется подниматься, вместо того чтобы молча стать открытым релеем.

```bash
GHP_ALLOW_ANONYMOUS=1 GHP_PREFIX=/ivanghproxy/ ./bin/gh-proxy
```

Сегмент с токеном из пути больше не выкусывается, так что URL — это просто
префикс и следом GitHub-URL:

```bash
BASE='https://sub.example.com/ivanghproxy'
curl -LO "$BASE/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz"
```

## Приватные репозитории без PAT

Для приватных репозиториев прокси нужен креденшел, который он предъявляет
GitHub. Вместо того чтобы выпускать PAT и вписывать его в `.env`, можно взять
токен у уже авторизованного `gh` CLI:

```bash
gh auth login                  # один раз, под тем пользователем, от которого работает прокси
GHP_UPSTREAM_TOKEN_SOURCE=gh   # в .env
```

Если `gh` не найден или разлогинен, прокси не стартует — это ошибка загрузки, а
не необъяснимый `404` на первой приватной скачке. Токен перечитывается раз в
`GHP_GH_REFRESH` (по умолчанию `5m`): gh ротирует OAuth-токены сам. Задать
одновременно `GHP_UPSTREAM_TOKEN` и этот источник — ошибка старта, выбирайте
что-то одно.

Ищется в двух местах, по порядку:

1. `gh auth token --hostname github.com`, если бинарник доступен;
2. `$GHP_GH_CONFIG_DIR/hosts.yml`, если нет.

Второй пункт — ровно то, что делает вариант рабочим в Docker: образ собран на
`scratch`, никакого `gh` внутри нет и запускать нечего, поэтому конфиг gh
монтируется внутрь read-only. Контейнер должен идти под uid, которому этот файл
читается (gh пишет его с правами `0600`):

```yaml
services:
  gh-proxy:
    user: "1000:1000"                    # uid, которому принадлежит ~/.config/gh
    volumes:
      - ${HOME}/.config/gh:/gh:ro
    environment:
      GHP_UPSTREAM_TOKEN_SOURCE: gh
      GHP_GH_CONFIG_DIR: /gh
```

Если gh держит токен в системном keyring, а не в `hosts.yml`, прочитать его
может только сам бинарник gh — тогда либо запускайте прокси вне контейнера, либо
оставайтесь на `GHP_UPSTREAM_TOKEN`.

Про размен стоит сказать прямо: так прокси получает доступ ко всему вашему
GitHub-аккаунту, а значит и любой обладатель `GHP_TOKEN` — тоже. PAT с правами
`contents: read` только на нужные репозитории строже. В обоих случаях
ограничивайте область через `GHP_ALLOW_LIST`.

## Настройки

Все — переменные окружения; полный список с комментариями в
[.env.example](.env.example).

| Переменная | По умолчанию | |
|---|---|---|
| `GHP_TOKEN` | — | **обязательно**, если не задан `GHP_ALLOW_ANONYMOUS=1`. Секрет, ≥16 символов, без `/?#` и пробелов |
| `GHP_TOKEN_FILE` | — | прочитать токен из файла (docker secrets) |
| `GHP_PREFIX` | `/` | точка монтирования, должна совпадать с `location` |
| `GHP_LISTEN` | `0.0.0.0:8899` | публичный listener |
| `GHP_ADMIN_LISTEN` | `127.0.0.1:8900` | `/healthz`, наружу не публикуется |
| `GHP_ALLOW_LIST` | пусто | `ivan`, `ivan/repo`, `*/repo` — пусто значит «любые» |
| `GHP_DENY_LIST` | пусто | то же, применяется после allow-листа |
| `GHP_DEFAULT_HOST` | пусто | хосты, которые подставляются, если в URL хоста нет, по порядку: `github.com,raw.githubusercontent.com`. См. [короткую форму](#короткая-форма) |
| `GHP_UPSTREAM_TOKEN` | — | GitHub PAT для приватных репозиториев и лимитов |
| `GHP_UPSTREAM_TOKEN_SOURCE` | `env` | `gh` — брать креденшел у авторизованного `gh` CLI. См. [приватные репозитории без PAT](#приватные-репозитории-без-pat) |
| `GHP_GH_BIN` / `GHP_GH_HOST` | `gh` / `github.com` | бинарник и хост GitHub для источника `gh` |
| `GHP_GH_CONFIG_DIR` | как у самого gh | где искать `hosts.yml`, если бинарник недоступен |
| `GHP_GH_REFRESH` | `5m` | как часто перечитывать токен у `gh`; `0` — никогда |
| `GHP_SIZE_LIMIT` | `0` | больше лимита → `302` на настоящий GitHub. `512MB`, `2GB` |
| `GHP_REDIRECT_HOSTS` | CDN GitHub | куда можно следовать за редиректом (**заменяет** дефолт) |
| `GHP_MAX_REDIRECTS` | `5` | |
| `GHP_STATUS_PATH` | пусто | путь [страницы статуса](#страница-статуса); пусто — страницы нет |
| `GHP_STATUS_AUTH` | `token` | `none` — проверку оставляем reverse-proxy |
| `GHP_CORS` | `0` | разрешить `fetch()` из браузера |
| `GHP_LOG_TARGETS` | `0` | писать URL-ы в лог (при токене в пути это лог секретов) |
| `GHP_ALLOW_ANONYMOUS` | `0` | выключить аутентификацию — открытый релей |

## Разработка

```bash
make test      # go test ./...
make race      # go test -race
make lint      # go vet + gofmt
make build     # bin/gh-proxy
make run       # локальный запуск с временным токеном
make token     # openssl rand -hex 24
```

Локально без Docker:

```bash
GHP_TOKEN=local-dev-token-0123456789 GHP_PREFIX=/ivanghproxy/ \
GHP_LISTEN=127.0.0.1:8899 ./bin/gh-proxy
```

### Образы

| Тег | Собирается |
|---|---|
| `ghcr.io/prettyleaf/gh-proxy:latest`, `:X.Y.Z` | по тегу `v*` ([docker.yml](.github/workflows/docker.yml)) |
| `ghcr.io/prettyleaf/gh-proxy:dev` | на каждый push в `dev`, если прошли тесты ([build-dev.yml](.github/workflows/build-dev.yml)) |
| `ghcr.io/prettyleaf/gh-proxy:dev-<sha>` | та же сборка, прибитая к коммиту |

Dev-образ сообщает версию как `dev-<sha>` — и в `/healthz`, и на странице
статуса, так что по запущенному контейнеру видно, из какого он коммита. Чтобы
сидеть на нём, поменяйте image в `docker-compose.yml` и обновляйтесь:

```bash
docker compose pull && docker compose up -d
```

Оба воркфлоу запускаются и руками из вкладки Actions (`workflow_dispatch`).
Секреты `TELEGRAM_TOKEN`, `TELEGRAM_CHAT_ID`, `TELEGRAM_TOPIC_ID` включают
уведомления о сборке; без них эти шаги молча пропускаются.

## Лицензия

MIT
