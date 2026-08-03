# gh-proxy

EN | [RU](README.ru.md)

[hunshcn/gh-proxy](https://github.com/hunshcn/gh-proxy) fork in Go combined with new features for personal preferences and purposes of other contributors or/and like-minded people. It mounts on a path rather than the domain root, so it lives next to an existing site; it never announces itself — anything unauthenticated is a plain `404`; and it is token-gated by default.

```
https://sub.example.com/ivanghproxy/TOKEN/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz
└──────── your site ───┘└─ prefix ──┘└───┘ └──────────────── an ordinary GitHub URL ──────────────────────┘
                                    token
```

## What it does

* releases, branch and tag archives, `blob`/`raw`, gists;
* `git clone` and `fetch` (git smart HTTP) with no git configuration;
  `push` works too, but only with a write-capable `GHP_UPSTREAM_TOKEN`: the client's own credentials are stripped and never reach GitHub;
* resumable and parallel downloads (`Range` passes through);
* server-side redirect following to GitHub's CDN backends;
* private repositories through your own PAT (`GHP_UPSTREAM_TOKEN`);
* restriction by owner/repository (allow and deny lists).

## Quick start

```bash
git clone https://github.com/prettyleaf/gh-proxy && cd gh-proxy

cp .env.example .env
openssl rand -hex 24 # set GHP_PREFIX in .env

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

Check:

```bash
BASE='https://sub.example.com/ivanghproxy/YOUR_TOKEN'

curl -LO "$BASE/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz"
git clone "$BASE/https://github.com/cli/browser"

curl -s -o /dev/null -w '%{http_code}\n' https://sub.example.com/ivanghproxy/   # 404
```

See [docs/nginx.md](docs/nginx.md) for more info.

## Documentation

The documents below are in Russian.

| | |
|---|---|
| [docs/nginx.md](docs/nginx.md) | mounting on a path: working config, every directive explained, verification, troubleshooting, Caddy/Traefik/Cloudflare |
| [docs/clients.md](docs/clients.md) | git, curl, wget, aria2, `insteadOf`, CI, Dockerfile |
| [docs/security.md](docs/security.md) | threat model: stealth 404, token leaks, SSRF boundaries, what never reaches GitHub |

## How the token is passed

Four equivalent ways:

```bash
# path segment — the primary one, and the only one git handles out of the box
curl "$BASE/https://raw.githubusercontent.com/cli/cli/trunk/README.md"

# headers — for curl/wget/CI
curl -H "Authorization: Bearer TOKEN"  "https://sub.example.com/ivanghproxy/https://..."
curl -u "x:TOKEN"                      "https://sub.example.com/ivanghproxy/https://..."
curl -H "X-Proxy-Token: TOKEN"         "https://sub.example.com/ivanghproxy/https://..."
```

The token belongs in the path because the proxy answers `404` rather than `401`: git makes its first request without credentials and waits for a `401 WWW-Authenticate` to learn that it should authenticate. A stealth 404 never issues that challenge — and a token already written into the URL makes the exchange unnecessary.

## Running without a token

`GHP_ALLOW_ANONYMOUS=1` disables authentication entirely. `GHP_TOKEN` must then
be empty; setting both is a startup error, as is setting neither — the proxy
refuses to boot rather than quietly turn into an open relay.

```bash
GHP_ALLOW_ANONYMOUS=1 GHP_PREFIX=/ivanghproxy/ ./bin/gh-proxy
```

No token segment is consumed from the path, so the URL is just the prefix
followed by the GitHub URL:

```bash
BASE='https://sub.example.com/ivanghproxy'
curl -LO "$BASE/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz"
```

## Settings

All of them are environment variables; the full annotated list is in
[.env.example](.env.example).

| Variable | Default | |
|---|---|---|
| `GHP_TOKEN` | — | **required** unless `GHP_ALLOW_ANONYMOUS=1`. Secret, ≥16 characters, no `/?#` or spaces |
| `GHP_TOKEN_FILE` | — | read the token from a file (docker secrets) |
| `GHP_PREFIX` | `/` | mount point, must match the nginx `location` |
| `GHP_LISTEN` | `0.0.0.0:8899` | public listener |
| `GHP_ADMIN_LISTEN` | `127.0.0.1:8900` | `/healthz`, never published |
| `GHP_ALLOW_LIST` | empty | `ivan`, `ivan/repo`, `*/repo` — empty means "any" |
| `GHP_DENY_LIST` | empty | same syntax, applied after the allow list |
| `GHP_UPSTREAM_TOKEN` | — | GitHub PAT for private repos and rate limits |
| `GHP_SIZE_LIMIT` | `0` | over the limit → `302` to the real GitHub. `512MB`, `2GB` |
| `GHP_REDIRECT_HOSTS` | GitHub CDNs | where redirects may be followed (**replaces** the default) |
| `GHP_MAX_REDIRECTS` | `5` | |
| `GHP_CORS` | `0` | allow `fetch()` from a browser |
| `GHP_LOG_TARGETS` | `0` | log upstream URLs (with a token in the path, that logs secrets) |
| `GHP_ALLOW_ANONYMOUS` | `0` | disable authentication — open relay |

## Development

```bash
make test      # go test ./...
make race      # go test -race
make lint      # go vet + gofmt
make build     # bin/gh-proxy
make run       # local run with a throwaway token
make token     # openssl rand -hex 24
```

Locally without Docker:

```bash
GHP_TOKEN=local-dev-token-0123456789 GHP_PREFIX=/ivanghproxy/ \
GHP_LISTEN=127.0.0.1:8899 ./bin/gh-proxy
```

## License

MIT
