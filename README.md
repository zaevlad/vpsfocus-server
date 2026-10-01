# vpsFocus server code

[Русская версия](README.ru.md)

[vpsFocus](https://vpsfocus.xyz) is a desktop app that installs web analytics
and monitoring on **your own** server over SSH. Its promise is simple: it
reads, it does not change your sites, and your visitors' data stays on your
server.

This repository lets you check that promise instead of taking our word for
it. It contains **everything vpsFocus puts on or runs on your server**:

| Directory | What it is |
|---|---|
| `bundle/<version>/` | The installer bundle exactly as your server receives it: `install.sh`, `update.sh`, `rollback.sh`, templates, the signed inventory `bundle.json` and its signature `bundle.sig` |
| `bundle/shared/` | `dry-run.sh` — the read-only readiness check the app runs before installing |
| `scripts/` | Every other script the app runs on your server over SSH (finding your sites, backups, the security check…) |
| [`SERVER-SIDE.md`](SERVER-SIDE.md) | The inventory: each script — when it runs, what it reads, what it changes |
| `agent/` | Source of the monitoring agent (Go) and its `Dockerfile` |
| `images/caddy/` | `Dockerfile` of our Caddy build (rate limiting and DNS modules) |
| `keys/release.pub` | The public half of the release key that signs every bundle |
| `tools/verify/` | One command that checks a release |

Analytics itself is [Umami](https://github.com/umami-software/umami) and the
database is PostgreSQL — their official images, pinned by digest in
`docker-compose.yml.tpl`.

## What is **not** here, honestly

* **The desktop app is closed source.** You can read every script here, but
  you cannot check from this repository alone that the closed app sends
  exactly these scripts. Inside the app they are compiled from these very
  files, and our automated tests fail if the app runs a script that is not
  in `SERVER-SIDE.md`. On your side, `auditd` or the `sudo` log on your server
  shows every command that was run.
* **Our website and backend** (accounts and the optional external uptime
  checks) are not here: they never run on your server.
* **Versions older than the first one published here** are not here. If your
  server runs an older version, the app offers an update — after it you are
  on a published version.
* **The first tag, `bundle-0.27.0`, is not exact for `agent/` and
  `images/caddy/`.** Its images (agent 0.22.0, Caddy 2.11.4-rl2) were built
  before this repository existed, from sources that differ from the tag only
  in five code comments and in pins we added to the Dockerfiles: Caddy
  modules pinned to the very versions inside the rl2 image, base images
  pinned by digest (the digests used for those builds are not recorded).
  The Go code is the same. From bundle 0.28.0 on, CI checks that every image
  was built from exactly the tree in the tag.

## Things you will want to know before reading

* **The agent sees the whole disk of the server, read-only** (`/:/host/root:ro`
  in `docker-compose.yml.tpl`). It needs it for web-server logs, configs,
  disk usage and the host name. It sends nothing anywhere except the alerts
  you configured (email, Telegram, Slack, Discord, webhook).
* The app changes exactly two things outside its own directory
  `/opt/vpsfocus`, both only after you agree and both reversible: a firewall
  rule for the monitoring port (`install.sh`) and, if you press the button,
  read access for the `adm` group to a log directory (`scripts/log-access.sh`).
* Code comments are in Russian, the team's working language. Some comments
  mention internal design documents (`misc/…`) that are not published.

## How to check a release

Every release is one commit and one tag `bundle-<version>`. The app's install
screen shows the bundle version and links to its tag.

**1. Check the signature and the files** (needs [Go](https://go.dev/dl/)):

```sh
git clone https://github.com/zaevlad/vpsfocus-server && cd vpsfocus-server
git checkout bundle-0.28.0
go run ./tools/verify -version 0.28.0
```

It checks that `bundle.sig` is a valid signature of `bundle.json` made with
the release key, that `bundle.json` lists exactly the files of the version
with the right SHA-256, and that every image is pinned by digest.

You can also check the signature with the standard
[minisign](https://jedisct1.github.io/minisign/) tool. `bundle.sig` is the
minisign signature encoded in base64 once more:

```sh
base64 -d bundle/0.28.0/bundle.sig > /tmp/bundle.minisig
minisign -V -p keys/release.pub -m bundle/0.28.0/bundle.json -x /tmp/bundle.minisig
```

**2. Compare with what your server got.** Download the archive the app
downloads and compare it file by file:

```sh
curl -fsSLo bundle-0.28.0.tar.gz https://vpsfocus.xyz/installer/bundle/0.28.0
go run ./tools/verify -version 0.28.0 -archive bundle-0.28.0.tar.gz
```

**3. Compare the images.** `bundle/<version>/docker-compose.yml.tpl` pins every
image by digest. On your server, `docker inspect` shows the same digests.
Starting with bundle 0.28.0, our images are built by GitHub Actions
in this repository with
[build provenance](https://docs.github.com/actions/security-for-github-actions/using-artifact-attestations)
— then `gh attestation verify oci://ghcr.io/zaevlad/vpsfocus-agent@sha256:… --repo zaevlad/vpsfocus-server`
proves which commit an image was built from. Until then, the agent and Caddy
images were built from the sources here on the maintainer's machine.

## The release key

Fingerprint: **`337F561861A23810`** (minisign key id). It is the same key that
signs updates of the vpsFocus app; the app refuses to install a bundle without
a valid signature. The same fingerprint is published on
[vpsfocus.xyz](https://vpsfocus.xyz/en/server-code) and in
[SECURITY.md](SECURITY.md). If it ever changes, it changes in all three places
with a note on the date and the reason.

## License

[Business Source License 1.1](LICENSE). In plain words: you may read, check,
modify and run this code on your servers and your clients' servers, including
commercially. You may not build a competing monitoring or analytics product
on it. Four years after each version is published, it becomes Apache 2.0.
This is source-available, not an OSI-approved open-source license.
Third-party software keeps its own license — see
[THIRD-PARTY.md](THIRD-PARTY.md).

## Issues, not pull requests

Questions and bug reports are welcome as issues. We do not merge pull
requests here: this repository mirrors released versions, and a change has
to go through the main project. Security problems — see
[SECURITY.md](SECURITY.md).
