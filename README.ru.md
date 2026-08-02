# gh-proxy

*[English version](README.md)*

Свой сервер перед GitHub. Припишите адрес прокси к любому `github.com`-URL — и
загрузка (релиз, raw-файл, архив ветки, `git clone`) пойдёт через вашу машину, а
не через канал клиента. Полезно там, где GitHub медленный, режется или
недоступен, и чтобы тянуть приватные репозитории на хосты, у которых нет
собственных креденшелов.

```
https://sub.example.com/ivanghproxy/ТОКЕН/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz
└──────── ваш сайт ────┘└─ префикс ─┘└───┘ └──────────────── обычный GitHub-URL ─────────────────────────┘
                                    токен
```

Переписанный на Go [hunshcn/gh-proxy](https://github.com/hunshcn/gh-proxy) с
тремя отличиями: монтируется **на путь**, а не на корень домена, поэтому живёт
рядом с уже работающим сайтом; **не выдаёт своего существования** — всё
неавторизованное это обычный `404`; и по умолчанию работает **только по токену**.

Один статический бинарь, ноль зависимостей кроме стандартной библиотеки Go,
образ на `scratch` весом ~6 МБ.

## Что умеет

* релизы, архивы веток и тегов, `blob`/`raw`, gist;
* `git clone` и `fetch` (git smart HTTP) — без настройки git;
  `push` тоже проходит, но только если задан `GHP_UPSTREAM_TOKEN` с правом
  записи: собственные креденшелы клиента до GitHub не доезжают, их вырезают;
* докачка и параллельная загрузка (`Range` проходит насквозь);
* серверное следование редиректам на CDN-бэкенды GitHub — клиенту не нужен
  доступ к `objects.githubusercontent.com`;
* приватные репозитории через собственный PAT (`GHP_UPSTREAM_TOKEN`);
* ограничение по владельцам/репозиториям (allow/deny-листы).

## Быстрый старт

```bash
git clone https://github.com/prettyleaf/gh-proxy && cd gh-proxy

cp .env.example .env
printf 'GHP_TOKEN=%s\n' "$(openssl rand -hex 24)" >> .env
$EDITOR .env                      # как минимум задайте GHP_PREFIX

docker compose up -d --build
```

Чтобы не собирать локально, укажите в `docker-compose.yml`
`image: ghcr.io/prettyleaf/gh-proxy:latest` и уберите блок `build:` — образы для
тегов публикуются в GHCR через
[.github/workflows/docker.yml](.github/workflows/docker.yml).

Затем в nginx — **точно так**, каждая строка важна:

```nginx
location = /ivanghproxy { return 404; }

location /ivanghproxy/ {
    proxy_pass http://127.0.0.1:8899;      # без слеша и без URI на конце!

    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host              $host;
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

**Почему `proxy_pass` без слеша, почему `proxy_buffering off`, почему
`access_log off` — [docs/nginx.md](docs/nginx.md).** Это основной документ:
именно там ломается монтирование на сабпуть.

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

Токен в пути нужен потому, что прокси отвечает `404`, а не `401`: git делает
первый запрос без креденшелов и ждёт `401 WWW-Authenticate`, чтобы понять, что
надо авторизоваться. Стелс-404 такого вызова не шлёт — а токен, уже вписанный в
URL, делает обмен ненужным.

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

Так стоит делать, только если доступ к листенеру уже ограничен чем-то другим —
VPN, IP-фильтром, mTLS или биндом на приватный интерфейс. В открытом интернете
анонимный инстанс — это ваш трафик для всех, кто его нашёл, и ваш IP в чужих
логах. Добавьте `GHP_ALLOW_LIST`, чтобы хотя бы ограничить, что через него можно
тянуть, и не задавайте `GHP_UPSTREAM_TOKEN`: без аутентификации этот PAT
подставляется в запросы любого желающего.

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

## Отличия от оригинала

| | hunshcn/gh-proxy | здесь |
|---|---|---|
| Язык | Python + Flask + uwsgi | Go, один бинарь, без зависимостей |
| Монтирование | только `/` (префикс есть лишь в CF Worker) | `GHP_PREFIX`, документировано для nginx |
| Доступ | открыт по умолчанию | по умолчанию нужен токен; открытый режим — только явным флагом |
| Ответ на отказ | `403` с текстом причины | `404`, одинаковый для всех причин |
| Главная страница | тянет HTML с `hunshcn.github.io` при старте | нет |
| Проверка URL | regex, `.+?` может проглотить слеш | точное сравнение хоста + разбор по сегментам |
| Заголовки | проксируются как есть | вырезаются `Set-Cookie`, `Authorization`, `Referer`, `X-Forwarded-*` |
| Редиректы | следует за любым хостом | только allow-list, только `GET`/`HEAD` |

**Выброшено намеренно:** редирект на jsDelivr (`jsdelivr`, `pass_list` в
оригинале). Он отправляет клиента на публичный CDN — для приватного прокси это
обнуляет и приватность, и смысл.

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
