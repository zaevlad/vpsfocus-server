# Third-party software

The [Business Source License](LICENSE) covers the vpsFocus code only. The
software below keeps its own license.

## Inside the agent image (`agent/`)

The agent binary is built with these Go modules (`agent/go.mod`):

| Module | Version | License |
|---|---|---|
| golang.org/x/net | v0.58.0 | BSD-3-Clause |
| golang.org/x/sys | v0.47.0 | BSD-3-Clause |
| golang.org/x/text | v0.41.0 | BSD-3-Clause |
| modernc.org/sqlite | v1.57.0 | BSD-3-Clause |
| modernc.org/libc | v1.74.4 | BSD-3-Clause |
| modernc.org/mathutil | v1.7.1 | BSD-3-Clause |
| modernc.org/memory | v1.11.0 | BSD-3-Clause |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause |
| github.com/dustin/go-humanize | v1.0.1 | MIT |
| github.com/mattn/go-isatty | v0.0.24 | MIT |
| github.com/ncruces/go-strftime | v1.0.0 | MIT |

The image also contains a prebuilt **[lychee](https://github.com/lycheeverse/lychee)**
link checker (Apache-2.0 or MIT), downloaded from its GitHub release with a
pinned SHA-256 (`agent/Dockerfile`); its license texts are in
`agent/third-party/lychee/`. We did not build it ourselves.

Base images: `golang` (build stage only) and `alpine`.

## Our Caddy image (`images/caddy/`)

[Caddy](https://github.com/caddyserver/caddy) (Apache-2.0), built with
`xcaddy` and these modules — each under the license in its repository:

* [mholt/caddy-ratelimit](https://github.com/mholt/caddy-ratelimit)
* [caddy-dns/cloudflare](https://github.com/caddy-dns/cloudflare)
* [caddy-dns/route53](https://github.com/caddy-dns/route53)
* [caddy-dns/digitalocean](https://github.com/caddy-dns/digitalocean)
* [caddy-dns/acmedns](https://github.com/caddy-dns/acmedns)

## Images used as they are

* [Umami](https://github.com/umami-software/umami) (MIT) — the analytics
* [PostgreSQL](https://www.postgresql.org/about/licence/) (PostgreSQL License)

Both are official images, pinned by digest in
`bundle/<version>/docker-compose.yml.tpl`.

## `tools/verify`

Uses [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) (BSD-3-Clause)
for BLAKE2b.
