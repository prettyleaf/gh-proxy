# clients.md

```bash
BASE='https://sub.example.com/ivanghproxy/ВАШ_ТОКЕН'
```

Общее правило: приписать `$BASE/` перед любым GitHub-URL.

```
https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz
                              ↓
$BASE/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz
```

## Скачивание файлов

```bash
curl -LO "$BASE/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz"
wget "$BASE/https://raw.githubusercontent.com/cli/cli/trunk/README.md"

# докачка и параллельная загрузка работают: Range проходит насквозь
curl -C - -LO "$BASE/https://github.com/.../big.tar.gz"
aria2c -x8 "$BASE/https://github.com/.../big.tar.gz"
```

Что можно проксировать:

| Тип | Пример |
|---|---|
| Релиз | `$BASE/https://github.com/user/repo/releases/download/v1.0/file.zip` |
| Архив ветки | `$BASE/https://github.com/user/repo/archive/refs/heads/main.zip` |
| Архив тега | `$BASE/https://github.com/user/repo/archive/v1.0.0.tar.gz` |
| Файл в ветке | `$BASE/https://github.com/user/repo/blob/main/README.md` |
| Raw | `$BASE/https://raw.githubusercontent.com/user/repo/main/README.md` |
| Gist | `$BASE/https://gist.githubusercontent.com/user/id/raw/file.py` |
| git | `$BASE/https://github.com/user/repo` |

`/blob/` автоматически превращается в `/raw/` — HTML-страницу вы не получите, только содержимое файла.

## git

```bash
git clone "$BASE/https://github.com/cli/cli"
```

Работает без какой-либо настройки git: токен уже в URL, а значит не нужен обмен `401 WWW-Authenticate`, которого прокси намеренно не делает.

Для существующего репозитория:

```bash
git remote set-url origin "$BASE/https://github.com/cli/cli"
```

### .gitconfig

Чтобы не приписывать префикс руками, положите в `~/.gitconfig`:

```ini
[url "https://sub.example.com/ivanghproxy/ВАШ_ТОКЕН/https://github.com/"]
    insteadOf = https://github.com/
```

После этого обычный `git clone https://github.com/cli/cli` пойдёт через прокси.

> Токен окажется в `~/.gitconfig` открытым текстом — `chmod 600 ~/.gitconfig`. Ещё он попадёт в `.git/config` каждого склонированного репозитория, так что следите, чтобы такой репозиторий не уехал куда-то с этим remote. Безопаснее добавить `pushInsteadOf` наоборот, чтобы push шёл напрямую на GitHub:
>
> ```ini
> [url "https://github.com/"]
>     pushInsteadOf = https://sub.example.com/ivanghproxy/ВАШ_ТОКЕН/https://github.com/
> ```

### Токен в заголовке вместо URL

Если не хотите видеть секрет в `.git/config`:

```bash
git config --global \
  http."https://sub.example.com/ivanghproxy/".extraHeader \
  "Authorization: Bearer ВАШ_ТОКЕН"

git clone https://sub.example.com/ivanghproxy/https://github.com/cli/cli
```

git подставит заголовок ко всем URL с этим префиксом. Работает, потому что `http.<url>.*` в git сопоставляется по префиксу пути, а не только по хосту.

## Другие способы передать токен

Прокси принимает токен в четырёх местах — любой из них равнозначен:

```bash
# 1. сегмент пути (основной; единственный, который работает с git из коробки)
curl "$BASE/https://raw.githubusercontent.com/cli/cli/trunk/README.md"

# 2. Bearer
curl -H "Authorization: Bearer ВАШ_ТОКЕН" \
     "https://sub.example.com/ivanghproxy/https://raw.githubusercontent.com/cli/cli/trunk/README.md"

# 3. HTTP Basic — токен как пароль (имя пользователя любое) или как имя
curl -u "x:ВАШ_ТОКЕН" \
     "https://sub.example.com/ivanghproxy/https://raw.githubusercontent.com/cli/cli/trunk/README.md"

# 4. отдельный заголовок
curl -H "X-Proxy-Token: ВАШ_ТОКЕН" \
     "https://sub.example.com/ivanghproxy/https://raw.githubusercontent.com/cli/cli/trunk/README.md"
```

## CI и Dockerfile

Токен — в секреты CI, не в репозиторий.

```dockerfile
# Dockerfile
ARG GH_BASE
RUN curl -fsSL "${GH_BASE}/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz" \
    | tar xz -C /opt
```

```yaml
# GitHub Actions
- run: curl -fsSL "$GH_BASE/https://github.com/..." -o tool.tar.gz
  env:
    GH_BASE: ${{ secrets.GH_PROXY_BASE }}
```

Через заголовок (не оставляет токен в истории команд и в слоях образа):

```bash
curl -fsSL -H "Authorization: Bearer $GH_PROXY_TOKEN" \
  "https://sub.example.com/ivanghproxy/https://github.com/..." -o tool.tar.gz
```

## Браузер и `fetch()`

Ссылку можно просто открыть — файл скачается. Для `fetch()` из скрипта на другом
домене нужен CORS: включите `GHP_CORS=1` (по умолчанию выключено).

## Что не проксируется

Намеренно, чтобы сервис не был универсальным релеем:

* `api.github.com` — REST/GraphQL API;
* HTML-страницы GitHub (`github.com/user/repo`, issues, PR);
* git-LFS: объекты лежат на `*.githubusercontent.com` по подписанным URL, но клиент LFS ходит на отдельный endpoint, который сюда не входит;
* любые не-GitHub хосты.

Всё это отдаёт `404`.
