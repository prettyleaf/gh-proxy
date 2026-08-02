# gh-proxy

*[Русская версия](README.ru.md)*

Put your own server in front of GitHub. Prefix any `github.com` URL with the
address of your proxy and the transfer — a release asset, a raw file, a branch
tarball, a `git clone` — goes through your machine instead of the client's
connection. Useful wherever GitHub is slow, throttled or unreachable, and for
pulling private repositories onto hosts that hold no credentials of their own.

```
https://sub.example.com/ivanghproxy/TOKEN/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz
└──────── your site ───┘└─ prefix ──┘└───┘ └──────────────── an ordinary GitHub URL ──────────────────────┘
                                    token
```

A rewrite of [hunshcn/gh-proxy](https://github.com/hunshcn/gh-proxy) in Go with
three differences: it mounts **on a path** rather than the domain root, so it
lives next to an existing site; it **never announces itself** — anything
unauthenticated is a plain `404`; and it is **token-gated by default**.

One static binary, no dependencies beyond the Go standard library, a `scratch`
image of ~6 MB.

## What it does

* releases, branch and tag archives, `blob`/`raw`, gists;
* `git clone` and `fetch` (git smart HTTP) with no git configuration;
  `push` works too, but only with a write-capable `GHP_UPSTREAM_TOKEN`: the
  client's own credentials are stripped and never reach GitHub;
* resumable and parallel downloads (`Range` passes through);
* server-side redirect following to GitHub's CDN backends — the client needs no
  access to `objects.githubusercontent.com`;
* private repositories through your own PAT (`GHP_UPSTREAM_TOKEN`);
* restriction by owner/repository (allow and deny lists).

## Quick start

```bash
git clone https://github.com/prettyleaf/gh-proxy && cd gh-proxy

cp .env.example .env
printf 'GHP_TOKEN=%s\n' "$(openssl rand -hex 24)" >> .env
$EDITOR .env                      # at minimum, set GHP_PREFIX

docker compose up -d --build
```

To skip the build, set `image: ghcr.io/prettyleaf/gh-proxy:latest` in
`docker-compose.yml` and drop the `build:` block — tagged releases are published
to GHCR by [.github/workflows/docker.yml](.github/workflows/docker.yml).

Then in nginx — **exactly like this**, every line matters:

```nginx
location = /ivanghproxy { return 404; }

location /ivanghproxy/ {
    proxy_pass http://127.0.0.1:8899;      # no trailing slash, no URI!

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

Check:

```bash
BASE='https://sub.example.com/ivanghproxy/YOUR_TOKEN'

curl -LO "$BASE/https://github.com/cli/cli/releases/download/v2.62.0/gh_2.62.0_linux_amd64.tar.gz"
git clone "$BASE/https://github.com/cli/browser"

curl -s -o /dev/null -w '%{http_code}\n' https://sub.example.com/ivanghproxy/   # 404
```

**Why `proxy_pass` without a slash, why `proxy_buffering off`, why
`access_log off` — [docs/nginx.md](docs/nginx.md).** That is the primary
document: mounting on a subpath is where this breaks.

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

The token belongs in the path because the proxy answers `404` rather than `401`:
git makes its first request without credentials and waits for a
`401 WWW-Authenticate` to learn that it should authenticate. A stealth 404 never
issues that challenge — and a token already written into the URL makes the
exchange unnecessary.

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

Only do this when something else already controls who reaches the listener — a
VPN, an IP allow list, mTLS, or a bind to a private interface. On the public
internet an anonymous instance is bandwidth for whoever finds it, and your IP in
someone else's logs. Pair it with `GHP_ALLOW_LIST` to at least bound what can be
fetched, and keep `GHP_UPSTREAM_TOKEN` unset: without authentication, that PAT
would be handed to every caller's requests.

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

## Differences from the original

| | hunshcn/gh-proxy | here |
|---|---|---|
| Language | Python + Flask + uwsgi | Go, one binary, no dependencies |
| Mounting | `/` only (a prefix exists in the CF Worker alone) | `GHP_PREFIX`, documented for nginx |
| Access | open by default | token required by default; open mode only via an explicit flag |
| Refusal | `403` with the reason in the body | `404`, identical for every reason |
| Landing page | fetches HTML from `hunshcn.github.io` at startup | none |
| URL validation | regex, `.+?` can swallow a slash | exact host comparison plus segment parsing |
| Headers | proxied as-is | `Set-Cookie`, `Authorization`, `Referer`, `X-Forwarded-*` stripped |
| Redirects | follows any host | allow-list only, `GET`/`HEAD` only |

**Deliberately dropped:** the jsDelivr redirect (`jsdelivr`, `pass_list` in the
original). It sends the client off to a public CDN, which for a private proxy
cancels both the privacy and the point.

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
