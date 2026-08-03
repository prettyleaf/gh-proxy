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
* ограничение по владельцам/репозиториям (allow/deny-листы).

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
| `GHP_UPSTREAM_TOKEN` | — | GitHub PAT для приватных репозиториев и лимитов |
| `GHP_SIZE_LIMIT` | `0` | больше лимита → `302` на настоящий GitHub. `512MB`, `2GB` |
| `GHP_REDIRECT_HOSTS` | CDN GitHub | куда можно следовать за редиректом (**заменяет** дефолт) |
| `GHP_MAX_REDIRECTS` | `5` | |
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

## Лицензия

MIT
