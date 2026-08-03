# Монтирование на путь за nginx

Задача: повесить прокси на `https://sub.example.com/ivanghproxy/` рядом с уже работающим сайтом, ничего в нём не сломав и не выдав факт существования сервиса.

## .conf

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name sub.example.com;

    server_tokens off;

    location = /ivanghproxy { return 404; }

    location /ivanghproxy/ {
        proxy_pass http://127.0.0.1:8899;

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
}
```

`GHP_PREFIX` в `.env` должен совпадать с `location`:
`GHP_PREFIX=/ivanghproxy/`.

## Почему именно так

If `proxy_pass` is specified without a URI, the request URI is passed to the server **in the same form as sent by a client** when the original request is processed, or the full **normalized** request URI is passed when processing the changed URI.

Проксируемый URL выглядит так:

```
/ivanghproxy/TOKEN/https://github.com/cli/cli/releases/download/v1/my%20file.zip
                        ↑↑                                          ↑↑↑
                     двойной слеш                            percent-encoding
```

«Same form as sent by a client» = сырой request-target: `//` на месте, `%20` не тронут. Ровно то, что нужно.

Стоит дописать `/` в конце `proxy_pass` — и nginx переключается на нормализованный URI: схлопывает `//` в `/` и перекодирует escape-последовательности. `https://github.com` превращается в `https:/github.com`, а имя файла с `%2F` внутри — в путь с настоящим слешем.

Любая из этих директив создаёт «changed URI», и nginx по той же цитате из документации начинает слать апстриму нормализованную форму — эффект точно такой же, как в пункте. В частности, не пытайтесь срезать префикс через

```nginx
location /ivanghproxy/ {
    rewrite ^/ivanghproxy/(.*) /$1 break;
    proxy_pass http://127.0.0.1:8899;
}
```

Префикс срезает само приложение — для этого и существует `GHP_PREFIX`.

Три директивы, без которых ломается git и большие файлы:

| Директива | Что будет без неё |
|---|---|
| `proxy_buffering off` | `git clone` виснет на согласовании pack-файла: обе стороны ждут байты, лежащие в буфере nginx |
| `proxy_request_buffering off` | nginx сначала целиком принимает тело `POST /git-upload-pack`, потом шлёт — на больших репах это таймаут |
| `client_max_body_size 0` | `413 Request Entity Too Large` на `git push` и на больших запросах upload-pack (дефолт всего 1 МБ) |

`proxy_read_timeout`/`proxy_send_timeout` подняты до часа, потому что при `git clone` большого репозитория GitHub может молчать несколько минут, пока считает pack.

При токене-в-пути (`/ivanghproxy/TOKEN/https://...`) дефолтный `combined` формат сохраняет секрет открытым текстом в файл, который читает кто угодно с доступом к серверу и который попадает в ротацию и бэкапы.

Варианты:

```nginx
access_log off;
```

или, если статистика всё же нужна:

```nginx
# в http {}
log_format ghproxy '$remote_addr $status $body_bytes_sent $request_time';

# в location
access_log /var/log/nginx/ghproxy.log ghproxy;
```

Само приложение URL-ы не логирует: `GHP_LOG_TARGETS=0` по умолчанию.

### robots.txt

```
Disallow: /ivanghproxy/     # это публикация секретного пути, а не защита
```

`robots.txt` читается всеми и первым делом. Для той же цели служит заголовок `X-Robots-Tag: noindex` — приложение ставит его само, в конфиге он продублирован на случай ответов, сгенерированных nginx.

> Обратите внимание: `add_header` в `location` отменяет все `add_header`,
> унаследованные с уровня `server`. Если у вас там HSTS и прочее — продублируйте
> их в этом блоке.

## Проверка

```bash
nginx -t && systemctl reload nginx

BASE='https://sub.example.com/ivanghproxy/ВАШ_ТОКЕН'

# 1. сервис молчит для всех, кто не знает секрета
curl -s -o /dev/null -w '%{http_code}\n' https://sub.example.com/ivanghproxy       # 404, НЕ 301
curl -s -o /dev/null -w '%{http_code}\n' https://sub.example.com/ivanghproxy/      # 404
curl -s -o /dev/null -w '%{http_code}\n' "https://sub.example.com/ivanghproxy/нет/https://github.com/cli/cli/tags"  # 404

# 2. префикс подключён правильно (это провалится при proxy_pass со слешем)
curl -sI "$BASE/https://raw.githubusercontent.com/cli/cli/trunk/README.md" | head -1   # 200

# 3. percent-encoding доезжает целым — главный индикатор пункта (1)
curl -s -o /dev/null -w '%{http_code}\n' \
  "$BASE/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz"  # 200

# 4. редирект на CDN отрабатывает на сервере, а не отдаётся вам
curl -s -o /dev/null -w '%{http_code} %{size_download}\n' \
  "$BASE/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz"  # 200 13065800

# 5. докачка
curl -s -r 0-99 -o /dev/null -w '%{http_code} %{size_download}\n' "$BASE/<любой релиз>"      # 206 100

# 6. git
git clone --depth 1 "$BASE/https://github.com/cli/browser" /tmp/probe && rm -rf /tmp/probe

# 7. в логах нет токена
sudo grep -c "ВАШ_ТОКЕН" /var/log/nginx/*.log    # 0
```

## Диагностика

| Симптом | Причина |
|---|---|
| 404 на всё, даже с верным токеном | `GHP_PREFIX` не совпадает с `location`. Он должен быть с обоими слешами: `/ivanghproxy/` |
| 404 от GitHub на файлы с пробелами/`+` в имени | `proxy_pass` со слешем на конце → nginx перекодировал `%XX`. Пункт (1) |
| `git clone` доходит до «Resolving deltas» и виснет | нет `proxy_buffering off` |
| `413` при `git clone` большого репо | нет `client_max_body_size 0` |
| `502` в браузере, в логе приложения `redirect to disallowed host` | GitHub перекинул на хост не из списка. Добавьте его в `GHP_REDIRECT_HOSTS` (полный список нужно указывать целиком — он заменяет дефолтный, а не дополняет) |
| `301` вместо `404` на `/ivanghproxy` | нет блока `location = /ivanghproxy { return 404; }` |
| Всё работает, но качается медленно и рывками | `proxy_buffering` включён где-то выше по конфигу |

Логи приложения:

```bash
docker compose logs -f gh-proxy
```

Для отладки временно: `GHP_LOG_LEVEL=debug` (покажет причину каждого 404) и
`GHP_LOG_TARGETS=1` (покажет upstream-URL в ошибках). **Оба возвращайте обратно
после отладки** — вместе они пишут в лог полные URL.

## Другие фронтенды

### Caddy

```caddyfile
sub.example.com {
    handle_path /ivanghproxy/* {
        # handle_path срезает префикс, поэтому приложение монтируем в корень
        reverse_proxy 127.0.0.1:8899 {
            flush_interval -1
        }
    }
}
```

При этом `GHP_PREFIX=/` (Caddy уже срезал префикс). Caddy нормализует путь и
схлопывает `//` — приложение чинит это само, но **percent-encoding Caddy
сохраняет**, так что имена файлов не страдают.

### Cloudflare перед nginx

Cloudflare нормализует URL и схлопнет `//`. Приложение это чинит, но учтите два
момента: Cloudflare кэширует ответы (включая приватные файлы — настройте Cache
Rules на bypass для этого пути) и обрывает соединение по таймауту 100 секунд на
свободном тарифе, чего может не хватить для большого `git clone`.

## Необязательное усиление

```nginx
# в http {}
limit_req_zone $binary_remote_addr zone=ghproxy:10m rate=10r/s;

# в location /ivanghproxy/
limit_req zone=ghproxy burst=40 nodelay;

# только из своей сети/VPN — если весь трафик идёт оттуда
allow 10.8.0.0/24;
allow 203.0.113.5;
deny all;
```
