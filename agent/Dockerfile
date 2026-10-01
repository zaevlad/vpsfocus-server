# Образ агента для VPS клиента.
#
# Собирается у нас и уезжает в реестр — по той же причине, что и свой Caddy:
# сборка на сервере клиента тянула бы модули Go и падала бы на сервере с
# закрытым исходящим трафиком, причём уже после того, как мы поставили туда
# Docker.
#
# Собирать и публиковать: agent/build.ps1 (с трека Q — GitHub Actions
# открытого репозитория, `build-images.yml`, с аттестацией происхождения).

# Базовые образы — по дайджесту индекса, а не по тегу (трек Q, П2): тег в
# реестре переписывают, и две сборки из одного коммита дали бы разный
# образ. Тег оставлен для чтения; обновить базу — сменить и тег, и дайджест
# (`docker buildx imagetools inspect <тег>`).

# --platform=$BUILDPLATFORM обязателен: без него стадия идёт под эмуляцией
# QEMU, где Go в длинных сборках зависает намертво (проверено 2026-09-21 на
# образе Caddy). GOARCH ниже и так делает кросс-сборку — эмулировать при
# этом нечего и незачем.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine@sha256:8ac98ca534ac3f51e1f420a1dd2c15e74c75cfa0f23f3ad27eb5d7236c349a0c AS builder

WORKDIR /src

# Сначала зависимости: слой с ними переживает правки кода и не качается
# заново на каждой сборке.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO выключен намеренно. Драйвер SQLite выбран чистый на Go (modernc)
# именно ради этого: с cgo кросс-сборка под arm64 требует тулчейна на
# каждую архитектуру, а без него хватает GOARCH.
#
# -s -w выбрасывают таблицы отладки: агент живёт на проде клиента, где
# каждый мегабайт занят его сайтом, а не нашими символами.
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /agent ./cmd/agent

# ── Lychee ────────────────────────────────────────────────────────────────
# Проверка битых ссылок. Берём готовый статический бинарник из релиза:
# musl-сборка живёт в alpine без лишних библиотек, а собирать Rust из
# исходников под две архитектуры ради одного бинарника — часы на сборку.
#
# Версия зафиксирована, архив сверяется с суммой релиза: подмена бинарника,
# который ходит по чужим сайтам с прод клиента, была бы подарком атакующему.
# Лицензия — Apache-2.0/MIT, тексты едут в образ рядом (third-party/lychee).
FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8 AS lychee
ARG LYCHEE_VERSION=0.24.2
ARG TARGETARCH=amd64
RUN apk add --no-cache curl \
    && case "$TARGETARCH" in \
         amd64) LYCHEE_TRIPLE=x86_64-unknown-linux-musl \
                && LYCHEE_SHA256=73657a111819a30c47c08352896796f23d64e4eb2b3ed39b6d32149241566fc5 ;; \
         arm64) LYCHEE_TRIPLE=aarch64-unknown-linux-musl \
                && LYCHEE_SHA256=5d0b0e3aeab240f41920c633a6eaf97599be6eedda034b36e858ede7dba5e535 ;; \
         *) echo "архитектура $TARGETARCH не поддерживается" >&2; exit 1 ;; \
       esac \
    && curl -fsSL -o /tmp/lychee.tar.gz \
         "https://github.com/lycheeverse/lychee/releases/download/lychee-v${LYCHEE_VERSION}/lychee-${LYCHEE_TRIPLE}.tar.gz" \
    && echo "${LYCHEE_SHA256}  /tmp/lychee.tar.gz" | sha256sum -c - \
    && tar -xzf /tmp/lychee.tar.gz -C /tmp "lychee-${LYCHEE_TRIPLE}/lychee" \
    && install -m 0755 "/tmp/lychee-${LYCHEE_TRIPLE}/lychee" /usr/local/bin/lychee \
    && lychee --version

FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8

# Корневые сертификаты нужны для RDAP, Telegram и проверки чужих
# сертификатов, tzdata — чтобы время в письмах не было UTC-загадкой.
#
# postgresql17-client — ради `pg_dump`: копию базы снимает агент, потому что
# сокет docker в контейнер на чужом проде не кладут ни ради какой функции, а
# cron на хосте запрещён инвариантом («за пределами каталога и Docker мы
# меняем ровно две вещи»). Версия клиента обязана быть не ниже версии
# сервера — и та и другая зафиксированы одним и тем же бандлом, поэтому
# разъехаться они могут только вместе с ним.
RUN apk add --no-cache ca-certificates tzdata postgresql17-client

# Агент не делает ничего, что требует root: читает смонтированный /proc,
# пишет в свой том и ходит по сети. Права root в контейнере на чужом проде —
# лишний риск без выгоды.
#
# Группа заводится отдельной строкой и с явным номером, а не подбирается
# busybox: по этому номеру приложение отдаёт агенту `probes.json` — файл с
# правами 640, который больше никто на сервере читать не должен. Подберись
# номер сам, ключевые адреса стали бы нечитаемыми, и круг молча не пошёл бы
# ни разу. Совпадение номера со скриптами установки закреплено тестом
# `stack::tests::agent_group_matches_the_image`.
RUN addgroup -g 10001 agent && adduser -D -u 10001 -G agent agent

# Каталог базы создаётся здесь и здесь же получает владельца.
#
# Это не косметика: docker переносит содержимое и владельца каталога из
# образа в пустой именованный том при первом запуске. Без этого том
# создаётся принадлежащим root, агент под своим пользователем не может в
# него писать и уходит в бесконечный перезапуск с «unable to open database
# file».
RUN mkdir -p /data && chown agent:agent /data

COPY --from=builder /agent /usr/local/bin/agent

# Lychee и его лицензии: распространяем чужой бинарник — возим с собой
# оба текста, как того требуют Apache-2.0 и MIT.
COPY --from=lychee /usr/local/bin/lychee /usr/local/bin/lychee
COPY third-party/lychee/LICENSE-MIT /usr/share/licenses/vpsfocus-lychee/LICENSE-MIT
COPY third-party/lychee/LICENSE-APACHE /usr/share/licenses/vpsfocus-lychee/LICENSE-APACHE

USER agent
EXPOSE 9101

ENTRYPOINT ["/usr/local/bin/agent"]
