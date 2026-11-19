#!/bin/sh
# Обновление установленного стека.
#
# Обновление нуждается в собственном откате, отдельном от установочного:
# риск другой. Новая версия Umami может потребовать миграции базы, и упавшая
# на середине миграция оставит данные в несогласованном состоянии. Откат
# установки просто удаляет всё; здесь удалять нечего — надо вернуть как было.
#
# Порядок: снимок базы и текущих конфигов → подмена файлов → новые образы →
# запуск → проверка. При сбое на любом шаге после подмены — возврат старых
# конфигов, старых образов и данных из снимка.
#
# Снимок хранится до следующего успешного обновления: если беда всплывёт
# через час после «успеха», человеку будет к чему вернуться руками.
#
# ── Как запускается ───────────────────────────────────────────────────────
# Приложение кладёт новые файлы в .staged и запускает этот скрипт. Почему не
# поверх сразу: тогда снимать снимок было бы уже поздно — старый
# docker-compose.yml с прежними версиями образов оказался бы перезаписан, и
# откатываться стало бы не на что.
#
# Протокол вывода тот же, что у install.sh: маркеры шагов в stdout.

set -eu

trap '' PIPE

STACK_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$STACK_DIR"

STAGED="$STACK_DIR/.staged"
ROLLBACK="$STACK_DIR/.rollback"

# Имя лога — от mktemp, а не постоянное (Б4 ревизии 2026-09-18): /tmp общий,
# и файл с заранее известным именем мог создать любой пользователь сервера,
# а root дописывал бы в него вывод обновления. mktemp создаёт новый файл с
# правами 600. Путь уходит маркером на шаге preflight.
#
# Не создался — останавливаемся до ловушки: ничего ещё не тронуто, а без
# файла лога перенаправления вывода docker и pg_dump ниже падали бы сами.
if ! LOG="$(mktemp /tmp/vpsfocus-update.XXXXXX)"; then
    printf '%s\n' '::step:preflight' '::fail:preflight:logUnavailable'
    exit 1
fi

# Группа пользователя, под которым работает агент в своём контейнере.
#
# Нужна одному файлу — `agent-config/probes.json`: его читает сам агент, а
# не docker, и не из-под root. Номер закреплён в образе (`agent/Dockerfile`)
# и в приложении (`stack.rs`), совпадение проверяется тестом.
AGENT_GID=10001

STEP=""
FAILED=0
# Дошли ли мы до подмены файлов. До неё откатывать нечего.
APPLIED=0
# Успел ли запуститься новый стек. Если да, миграции могли пройти, и базу
# надо возвращать из снимка.
STARTED=0

WAIT_UMAMI_SECONDS=${WAIT_UMAMI_SECONDS:-240}
WAIT_AGENT_SECONDS=${WAIT_AGENT_SECONDS:-60}

marker() {
    printf '%s\n' "$1" >>"$LOG" 2>/dev/null || true
    printf '%s\n' "$1" 2>/dev/null || true
}

step() { STEP="$1"; marker "::step:$1"; }
finished() { marker "::done:$STEP"; }
info() { marker "::info:$STEP:$1"; }

die() {
    FAILED=1
    marker "::fail:$STEP:$1"
    exit 1
}

has() { command -v "$1" >/dev/null 2>&1; }

# ── Застрявшие контейнеры ─────────────────────────────────────────────────
# Пересоздание сети во время обновления оставляет часть контейнеров в старой:
# Umami перестаёт находить `postgres` и уходит в цикл падений. `docker compose
# up -d` этого не чинит — с его точки зрения контейнер в порядке.
#
# Считаем перезапуски, а не смотрим на состояние: контейнер в цикле падений
# `docker compose ps` показывает как running, застать его в момент падения
# почти не выходит.
heal_stuck() {
    stuck=""
    for service in $(docker compose config --services 2>/dev/null); do
        container="$(docker compose ps -aq "$service" 2>/dev/null | head -n1)"
        [ -n "$container" ] || continue
        restarts="$(docker inspect -f '{{.RestartCount}}' "$container" 2>/dev/null || echo 0)"
        if [ "${restarts:-0}" -ge 3 ]; then
            stuck="$stuck $service"
        fi
    done

    [ -n "$stuck" ] || return 1

    # shellcheck disable=SC2086
    docker compose up -d --force-recreate $stuck >>"$LOG" 2>&1 || true
    return 0
}

# Ждёт, пока панель ответит, и один раз лечит застрявшие контейнеры.
# Возвращает 0, если дождались. Аргумент — под каким шагом показывать
# подробности: откат печатает свои под именем `rollback`.
wait_for_umami() {
    waited=0
    healed=0
    until curl -fsS -o /dev/null "http://127.0.0.1:$UMAMI_HOST_PORT/api/heartbeat" 2>/dev/null; do
        waited=$((waited + 3))

        # Пересоздаём только застрявшие и только один раз: пересоздавать
        # здоровые контейнеры значит ронять аналитику клиента на ровном
        # месте.
        if [ "$healed" -eq 0 ] && [ "$waited" -ge 45 ]; then
            healed=1
            if heal_stuck; then
                marker "::info:$1:recreatingStuck"
            fi
        fi

        [ "$waited" -lt "$WAIT_UMAMI_SECONDS" ] || return 1
        sleep 3
    done
    return 0
}

# ── Возврат как было ──────────────────────────────────────────────────────

restore() {
    if [ "$APPLIED" -eq 0 ]; then
        # Файлы ещё не подменены, стек работает на старом. Трогать нечего.
        marker "::rollback:skipped"
        return
    fi

    marker "::rollback:started"

    # Валим стек целиком, а не по контейнеру.
    #
    # **Без `-v`**: тома остаются, а в них аналитика клиента за всё время.
    # Именно `-v` запрещён правилом про откат, а не `down` сам по себе.
    #
    # Целиком — потому что неудачное обновление пересоздаёт сеть, и часть
    # контейнеров остаётся в старой: Umami перестаёт находить `postgres` и
    # уходит в цикл падений. Поднимать их по одному поверх этого — гадание,
    # `down` и чистый подъём — определённость. Базу это не роняет: она в
    # томе, а `restore_database` поднимет её сам.
    docker compose down --remove-orphans >>"$LOG" 2>&1 || true

    # Конфиги — обратно. Вместе с ними возвращаются и версии образов: они
    # записаны в docker-compose.yml, а не приходят откуда-то ещё.
    for name in docker-compose.yml Caddyfile .env agent.env; do
        if [ -f "$ROLLBACK/$name" ]; then
            cp -p "$ROLLBACK/$name" "$STACK_DIR/$name"
        fi
    done
    if [ -d "$ROLLBACK/agent-config" ]; then
        rm -rf "$STACK_DIR/agent-config"
        cp -rp "$ROLLBACK/agent-config" "$STACK_DIR/agent-config"
    fi
    if [ -d "$ROLLBACK/tls" ]; then
        rm -rf "$STACK_DIR/tls"
        cp -rp "$ROLLBACK/tls" "$STACK_DIR/tls"
    fi

    # Данные — из снимка, но только если новый стек успел подняться: до
    # этого момента миграции не запускались, и база не тронута.
    if [ "$STARTED" -eq 1 ] && [ -f "$ROLLBACK/umami.sql.gz" ]; then
        if restore_database; then
            marker "::info:rollback:databaseRestored"
        else
            # Худший исход: конфиги вернули, а данные — нет. Молчать об этом
            # нельзя, снимок при этом остаётся на месте.
            marker "::rollback:failed"
            marker "::info:rollback:databaseNotRestored"
            docker compose up -d >>"$LOG" 2>&1 || true
            return
        fi
    fi

    if ! docker compose up -d --remove-orphans >>"$LOG" 2>&1; then
        marker "::rollback:failed"
        return
    fi

    # `up -d` возвращается, когда контейнеры запущены, а не когда панель
    # отвечает. Объяви мы откат законченным здесь — приложение вернулось бы
    # на экран аналитики и показало ошибку связи с панелью на исправном
    # стеке. «Откат закончен» должно означать «клиент снова видит
    # аналитику».
    if wait_for_umami rollback; then
        marker "::rollback:done"
    else
        marker "::rollback:failed"
        marker "::info:rollback:umamiNotBack"
    fi
}

restore_database() {
    # Пересоздаём базу целиком: накатить дамп поверх мигрированной схемы
    # нельзя, таблицы уже другие.
    docker compose up -d postgres >>"$LOG" 2>&1 || return 1

    waited=0
    until docker compose exec -T postgres pg_isready -U umami -d postgres >/dev/null 2>&1; do
        waited=$((waited + 2))
        [ "$waited" -lt 60 ] || return 1
        sleep 2
    done

    docker compose exec -T postgres psql -U umami -d postgres \
        -c 'drop database if exists umami with (force)' >>"$LOG" 2>&1 || return 1
    docker compose exec -T postgres psql -U umami -d postgres \
        -c 'create database umami' >>"$LOG" 2>&1 || return 1

    gzip -dc "$ROLLBACK/umami.sql.gz" \
        | docker compose exec -T postgres psql -U umami -d umami >>"$LOG" 2>&1
}

on_exit() {
    status=$?
    trap '' EXIT HUP INT TERM PIPE

    if [ "$status" -eq 0 ]; then
        return 0
    fi

    if [ "$FAILED" -eq 0 ]; then
        marker "::fail:${STEP:-preflight}:unexpectedError"
    fi

    restore
}
trap on_exit EXIT

trap 'FAILED=1; marker "::fail:${STEP:-preflight}:interrupted"; exit 1' HUP INT TERM

# ── 1. Проверка ───────────────────────────────────────────────────────────

step preflight

# Где искать лог, если обновление и его откат не пройдут (Б4).
info "log:$LOG"

[ "$(id -u)" = "0" ] || die notRoot

# Обновлять нечего, если ничего не установлено. Это не придирка: без
# stack-version.json мы не знаем, с какой версии обновляемся, и откатываться
# будет не на что.
[ -f "$STACK_DIR/stack-version.json" ] || die notInstalled

[ -d "$STAGED" ] || die missingFile
for required in docker-compose.yml Caddyfile .env agent.env \
                agent-config/sites.json agent-config/vitals.json \
                agent-config/logs.json agent-config/projects.json \
                agent-config/probes.json; do
    [ -f "$STAGED/$required" ] || die missingFile
done

set -a
# shellcheck disable=SC1091
. "$STACK_DIR/.env"
set +a

for name in STACK_NAME STACK_VERSION TRACKING_DOMAIN UMAMI_HOST_PORT \
            AGENT_HOST_PORT POSTGRES_PASSWORD; do
    eval "value=\${$name:-}"
    [ -n "$value" ] || die missingVariable
done

# Был ли у стека рабочий сертификат до обновления.
#
# Нужно для проверки в конце. Стек, поставленный с ещё не приехавшим
# сертификатом (порт занят, DNS не разошёлся), обновляется как любой другой,
# и требовать от него ответа по HTTPS нельзя — обновление откатилось бы из-за
# того, что было сломано и до него. А вот если сертификат работал и после
# обновления перестал — это регрессия, и откат ровно для неё.
#
# Стеки версии 0.1.0 этого поля не знают: у них Caddy стоял на 443 и без
# сертификата не работал вовсе, поэтому отсутствие поля означает «был».
CERT_WAS_READY=1
if grep -q '"certificateReady"[[:space:]]*:[[:space:]]*false' \
        "$STACK_DIR/stack-version.json" 2>/dev/null; then
    CERT_WAS_READY=0
fi

has curl || die curlMissing
has docker || die dockerDaemonDown
docker info >>"$LOG" 2>&1 || die dockerDaemonDown

finished

# ── 2. Снимок ─────────────────────────────────────────────────────────────
# Всё, к чему придётся возвращаться: данные и конфиги вместе с версиями
# образов. Снимаем до единого изменения.

step snapshot

rm -rf "$ROLLBACK"
mkdir -p "$ROLLBACK"
chmod 700 "$ROLLBACK"

for name in docker-compose.yml Caddyfile .env agent.env stack-version.json; do
    if [ -f "$STACK_DIR/$name" ]; then
        cp -p "$STACK_DIR/$name" "$ROLLBACK/$name"
    fi
done
if [ -d "$STACK_DIR/agent-config" ]; then
    cp -rp "$STACK_DIR/agent-config" "$ROLLBACK/agent-config"
fi
# Свой сертификат клиента (правка 1б). Обновление его не подменяет — в
# списке подмены ниже его нет, — но в снимке он обязан быть: откат
# возвращает прежний Caddyfile, и тот сошлётся на эти файлы.
if [ -d "$STACK_DIR/tls" ]; then
    cp -rp "$STACK_DIR/tls" "$ROLLBACK/tls"
fi

# Дамп базы. Без --clean и --create: восстанавливаем в свежесозданную базу,
# и лишние команды в дампе только мешают.
if ! docker compose exec -T postgres pg_dump -U umami -d umami 2>>"$LOG" \
        | gzip -c > "$ROLLBACK/umami.sql.gz"; then
    die snapshotFailed
fi

# Пустой дамп означает, что pg_dump отработал вхолостую: возвращаться по
# такому снимку — то же, что стереть аналитику клиента.
if [ ! -s "$ROLLBACK/umami.sql.gz" ]; then
    die snapshotFailed
fi

info "snapshotSize:$(wc -c < "$ROLLBACK/umami.sql.gz")"

finished

# ── 3. Подмена файлов ─────────────────────────────────────────────────────

step apply

APPLIED=1

for name in docker-compose.yml Caddyfile .env agent.env; do
    cp -p "$STAGED/$name" "$STACK_DIR/$name"
done
rm -rf "$STACK_DIR/agent-config"
cp -rp "$STAGED/agent-config" "$STACK_DIR/agent-config"

chown -R root:root "$STACK_DIR/docker-compose.yml" "$STACK_DIR/Caddyfile" \
    "$STACK_DIR/.env" "$STACK_DIR/agent.env" "$STACK_DIR/agent-config" 2>/dev/null || true
chmod 600 "$STACK_DIR/.env" "$STACK_DIR/agent.env"
# Ключевые адреса — тоже секрет, но читает их сам агент, а он работает в
# контейнере под своим пользователем, а не под root. Права 600 root:root
# означали бы «не читает никто», и круг по ключевым адресам не пошёл бы ни
# разу — молча, потому что файл на месте. Отсюда 640 и группа агента числом:
# такой группы на сервере клиента нет, а ядро сверяет номера.
chown root:"$AGENT_GID" "$STACK_DIR/agent-config/probes.json"
chmod 640 "$STACK_DIR/agent-config/probes.json"
chmod 644 "$STACK_DIR/docker-compose.yml" "$STACK_DIR/Caddyfile" \
          "$STACK_DIR/agent-config/sites.json" \
          "$STACK_DIR/agent-config/vitals.json" \
          "$STACK_DIR/agent-config/logs.json" \
          "$STACK_DIR/agent-config/projects.json"
chmod 755 "$STACK_DIR/agent-config"

# Скрипты обновляются последними и по одному: тот, что сейчас выполняется,
# менять на ходу нельзя — оболочка читает его с диска по мере выполнения.
for script in install.sh rollback.sh update.sh; do
    if [ -f "$STAGED/$script" ]; then
        cp -p "$STAGED/$script" "$STACK_DIR/$script.new"
        chmod 755 "$STACK_DIR/$script.new"
    fi
done

# Дальше работает уже новый конфиг, и переменные нужны из него. Главная —
# EDGE_PORT: у стеков версии 0.1.0 его в `.env` не было вовсе, потому что
# Caddy стоял на 443 жёстко. Приложение переносит порт как есть, а нам
# остаётся не спутать старое значение с новым.
set -a
# shellcheck disable=SC1091
. "$STACK_DIR/.env"
set +a
EDGE_PORT=${EDGE_PORT:-443}

finished

# ── 4. Образы ─────────────────────────────────────────────────────────────

step pull

pulled=0
attempt=1
while [ "$attempt" -le 3 ]; do
    if docker compose pull --quiet --policy missing; then
        pulled=1
        break
    fi
    info "imagePullRetry:$attempt"
    attempt=$((attempt + 1))
    sleep 5
done
[ "$pulled" = "1" ] || die imagePullFailed

finished

# ── 5. Запуск ─────────────────────────────────────────────────────────────

step start

STARTED=1
docker compose up -d --remove-orphans || die composeUpFailed

# Caddyfile примонтирован файлом, и `docker compose up -d` не перезапускает
# контейнер, если описание сервиса не изменилось. Новая настройка так и не
# доедет до Caddy: обновление отчиталось бы об успехе, а наружу продолжила
# бы смотреть старая. Поймано тестом, где новая версия конфига намеренно
# дырявая, — обновление её не заметило.
#
# Сначала перезагрузка, потому что она без разрыва: трекинг на проде клиента
# не должен моргать из-за обновления. Не вышло — перезапуск.
docker compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile >>"$LOG" 2>&1 \
    || docker compose restart caddy >>"$LOG" 2>&1 \
    || die composeUpFailed

finished

# ── 6. Ожидание ───────────────────────────────────────────────────────────
# Новая версия Umami при первом запуске накатывает миграции. Это самое
# опасное место обновления: именно здесь данные могут остаться на полпути.

step wait

wait_for_umami wait || die umamiNotReady
info "umamiReadyIn:$waited"

waited=0
until curl -fsS -o /dev/null "http://127.0.0.1:$AGENT_HOST_PORT/health" 2>/dev/null; do
    waited=$((waited + 3))
    [ "$waited" -lt "$WAIT_AGENT_SECONDS" ] || die agentNotReady
    sleep 3
done

finished

# ── 7. Проверка ───────────────────────────────────────────────────────────

step verify

docker compose exec -T postgres pg_isready -U umami -d umami >>"$LOG" 2>&1 \
    || die postgresNotReady

edge() {
    curl -sS -o /dev/null -k --max-time 15 \
        --resolve "$TRACKING_DOMAIN:$EDGE_PORT:127.0.0.1" \
        -w '%{http_code}' "https://$TRACKING_DOMAIN:$EDGE_PORT$1" 2>/dev/null || echo 000
}

answered=0
waited=0
while [ "$waited" -lt 90 ]; do
    if [ "$(edge /script.js)" = "200" ]; then
        answered=1
        break
    fi
    waited=$((waited + 3))
    sleep 3
done

if [ "$answered" = "1" ]; then
    for closed in /login /api/websites /; do
        [ "$(edge "$closed")" = "404" ] || die whitelistLeaks
    done
elif [ "$CERT_WAS_READY" = "1" ]; then
    # Работало до обновления и перестало после — это то, ради чего откат и
    # существует.
    die trackingUnavailable
else
    # Сертификата не было и до обновления. Обновление тут ни при чём, и
    # откатывать исправное обновление из-за старой беды нельзя.
    info certificatePending
fi

finished

# ── 8. Готово ─────────────────────────────────────────────────────────────

step finish

# Теперь можно заменить и скрипты: обновление позади.
for script in install.sh rollback.sh update.sh; do
    if [ -f "$STACK_DIR/$script.new" ]; then
        mv "$STACK_DIR/$script.new" "$STACK_DIR/$script"
    fi
done

images="$(docker compose config --images 2>/dev/null | sed 's/.*/"&"/' | paste -sd, - || echo '')"
previous="$(sed -n 's/.*"stackVersion"[^"]*"\([^"]*\)".*/\1/p' "$ROLLBACK/stack-version.json" 2>/dev/null || echo '')"

cat > "$STACK_DIR/stack-version.json" <<JSON
{
  "stackVersion": "$STACK_VERSION",
  "stackName": "$STACK_NAME",
  "installedAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "updatedFrom": "$previous",
  "trackingDomain": "$TRACKING_DOMAIN",
  "edgePort": $EDGE_PORT,
  "certificateReady": $([ "$answered" = "1" ] && echo true || echo false),
  "umamiHostPort": $UMAMI_HOST_PORT,
  "agentHostPort": $AGENT_HOST_PORT,
  "images": [$images]
}
JSON
chmod 644 "$STACK_DIR/stack-version.json"

rm -rf "$STAGED"

# Снимок остаётся до следующего обновления: беда может всплыть через час
# после «успеха», и человеку нужно, к чему вернуться.
info "snapshotKept"

finished

marker "::ready"

rm -f "$LOG" 2>/dev/null || true
