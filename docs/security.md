# security.md

* Всё неавторизованное — `404`.** Не `401`, не `403`. Неверный токен, отсутствующий токен, не-GitHub URL, чужой префикс — один и тот же ответ, одно и то же тело. Различия в ответах были бы оракулом: по ним подбирается и факт существования сервиса, и корректность токена.
* Нет `WWW-Authenticate`. Браузер не покажет окно ввода пароля, сканер не увидит признака защищённого ресурса.
* Нет главной страницы. Оригинальный gh-proxy на старте скачивает HTML с `hunshcn.github.io` и отдаёт его на `/` вместе с favicon. Здесь этого нет вообще: корень — `404`.
* `/healthz` живёт на отдельном порту** (`GHP_ADMIN_LISTEN`, по умолчанию `127.0.0.1:8900`) и никогда не публикуется наружу. Health-check под публичным префиксом был бы надёжным способом подтвердить, что сервис есть.
* `X-Robots-Tag: noindex, nofollow, noarchive` на всех ответах.
* Секретный префикс — первый слой. `/ivanghproxy/` угадать сложнее, чем `/`.

При старте сервис отказывается запускаться, если:

* токена нет и не выставлен явно `GHP_ALLOW_ANONYMOUS=1`;
* токен короче 16 символов;
* токен содержит `/`, `?`, `#` или пробел (он едет одним сегментом пути).

Генерация:

```bash
openssl rand -hex 24
```

| Канал | Статус |
|---|---|
| `access_log` nginx | `access_log off` в блоке `location`, см. [nginx.md](nginx.md) |
| Логи приложения | URL-ы не логируются, `GHP_LOG_TARGETS=0` по умолчанию |
| Заголовок `Referer` к GitHub | `Referer` вырезается перед отправкой апстриму |
| `Authorization` к GitHub | вырезается, наверх уходит только `GHP_UPSTREAM_TOKEN`, если он задан |
| `.git/config` после клона | см. предупреждение в [clients.md](clients.md) |

* Хост сверяется с фиксированным списком: `github.com`, `www.github.com`, `raw.githubusercontent.com`, `raw.github.com`, `gist.github.com`, `gist.githubusercontent.com`. Сравнение точное, поэтому `github.com.evil.example` и `evilgithub.com` не проходят.
* Путь должен совпасть с одной из поддерживаемых форм (релиз, архив, blob/raw, git smart HTTP, tags, raw, gist). `github.com/cli/cli` — уже нет.
* Редиректы следуются только на `GET`/`HEAD`, не более `GHP_MAX_REDIRECTS` раз, и только на хосты из `GHP_REDIRECT_HOSTS` (по умолчанию — CDN-бэкенды GitHub). Редирект на любой другой хост → `502`, тело ответа его не называет.
* Креденшелы, вписанные в целевой URL (`https://user:pass@github.com/...`), отбрасываются.

`GHP_DEFAULT_HOST` (короткая форма, по умолчанию выключена) границу SSRF не
двигает: подставить можно только хост из того же фиксированного списка, а сам
список задаётся в конфиге, не клиентом. Подстановка работает исключительно
тогда, когда хост не указан вообще — `gitlab.com/a/b/...` не перечитывается как
`owner=gitlab.com`, а отклоняется, как и раньше; `github.com/...` не
переписывается на `raw.githubusercontent.com`. Отличить одно от другого просто:
имя владельца на GitHub не содержит точки или двоеточия, а имя хоста содержит.

Что короткая форма всё-таки меняет — это площадь: под точкой монтирования
начинает отвечать любой путь вида `/owner/repo/...`. Токен и префикс срезаются
раньше подстановки, так что доступ по-прежнему закрыт, а allow/deny-листы
применяются к уже вычисленным `owner`/`repo`. Но при `GHP_PREFIX=/` на домене,
где живёт что-то ещё, прокси перехватит настоящие пути сайта — для зеркала
лучше отдельный префикс или поддомен.

Из исходящего запроса вырезаются: `Authorization`, `Cookie`, `X-Proxy-Token`,
`X-Real-IP`, `Forwarded`, все `X-Forwarded-*` и `Referer`.

`X-Forwarded-For` намеренно не проставляется (`SetXForwarded()` не вызывается).

Из ответа вырезаются `Set-Cookie`, `Content-Security-Policy`, `Content-Security-Policy-Report-Only`, `Clear-Site-Data`.

Даже с валидным токеном можно сузить, что вообще проксируется:

```bash
GHP_ALLOW_LIST=ivan
GHP_ALLOW_LIST=ivan,octocat/hello
GHP_DENY_LIST=*/secret-stuff
```

Сначала применяется allow-list (пустой = без ограничений), затем deny-list —
порядок как в оригинальном проекте. Всё отклонённое — `404`.

`GHP_UPSTREAM_TOKEN` — это GitHub PAT, который прокси предъявляет GitHub, а не клиентам. Он позволяет тянуть приватные репозитории и поднимает лимиты API.

Последствие: любой, у кого есть `GHP_TOKEN`, получает доступ ко всему, к чему имеет доступ этот PAT. Поэтому:

* давайте PAT минимальные права (`contents: read` для нужных репозиториев);
* сузьте область через `GHP_ALLOW_LIST`;
* при кросс-хостовом редиректе `Authorization` снимается автоматически — PAT не уедет на CDN-бэкенд (который его и так отверг бы: подпись у него в query).

Образ собирается на `scratch`: ни шелла, ни пакетного менеджера, ни утилит — разворачиваться в скомпрометированном контейнере не с чем. В compose-файле: `read_only: true`, `cap_drop: ALL`, `no-new-privileges`, uid 65534, публикация порта только на `127.0.0.1`.

* Не аутентифицирует пользователей по отдельности. Токен один на всех, кому вы его дали. Кто именно скачал файл — по логам не восстановить (и это осознанный размен: логов почти нет).
* Не ограничивает скорость и объём сам по себе. Если это нужно — `limit_req` в nginx, см. [nginx.md](nginx.md).
* Не прячет трафик от GitHub. GitHub видит запросы с IP вашего сервера.
* Не защищает от вас самих: `GHP_ALLOW_ANONYMOUS=1` превращает сервис в открытый релей. Включайте, только если доступ к listener'у ограничен чем-то ещё (VPN, IP allow-list, mTLS).

Pair it with `GHP_ALLOW_LIST` to at least bound what can be fetched, and keep `GHP_UPSTREAM_TOKEN` unset: without authentication, that PAT would be handed to every caller's requests. Only do this when something else already controls who reaches the listener — a VPN, an IP allow list, mTLS, or a bind to a private interface. On the public internet an anonymous instance is bandwidth for whoever finds it, and your IP in someone else's logs.