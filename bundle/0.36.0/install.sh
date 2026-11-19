#!/bin/sh
# Установка мониторингового стека на VPS клиента.
#
# Скрипт запускается от root в каталоге, куда приложение уже положило
# docker-compose.yml, Caddyfile и .env. Сам он ничего не генерирует: секреты
# создаются на машине агентства и приезжают в .env, а не рождаются здесь.
#
# POSIX sh, а не bash: наличие bash на сервере не гарантировано, и dry-run
# проверяет его отдельным фактом.
#
# Идемпотентность обязательна. Повторный запуск на работающем стеке не должен
# ни ломать его, ни менять пароли, ни трогать данные: установку перезапускают
# после обрыва связи, и это нормальный сценарий, а не авария.
#
# ── Протокол вывода ───────────────────────────────────────────────────────
# В stdout уходят маркеры, которые приложение разбирает на лету и показывает
# как пошаговый прогресс. Всё остальное — обычный вывод команд, он идёт
# рядом и показывается как технические подробности.
#
# С версии 0.16.0 приложение не держит этот скрипт в сессии SSH: оно
# запускает его отвязанным (`setsid`), перенаправив stdout и stderr в журнал
# прогона (`/opt/vpsfocus.run/log`), и читает журнал отдельным
# подключением — с байтового смещения, чтобы после обрыва связи продолжить
# с того же места. Для самого скрипта ничего не меняется: он печатает те же
# маркеры в тот же stdout.
#
# Подробный вывод долгих шагов сюда не идёт: apt печатает сотни строк, и
# экран бы утонул. Он уходит в $LOG — экран называет код, лог хранит
# подробности.
#
#   ::step:<шаг>              шаг начат
#   ::done:<шаг>              шаг закончен
#   ::info:<шаг>:<текст>      подробность шага
#   ::fail:<шаг>:<код>        шаг провалился, код — для перевода в интерфейсе
#   ::rollback:<состояние>    started | done | failed | skipped
#   ::ready                   установка завершена
#
# Шаг `domain` — единственный, который умеет закончиться ничем, не провалив
# установку. Сертификат выпускается через DNS, DNS расходится минутами, а
# иногда десятками минут, и держать всё это время прогресс ради последнего
# шага незачем: стек к этому моменту уже работает. Тогда шаг печатает
# `::info:domain:certificatePending`, установка доходит до конца, а Caddy
# продолжает попытки сам.
#
# Причина кодов вместо текста та же, что в Rust и на backend: интерфейс
# двуязычный, а бандл обновляется отдельно от приложения.
#
# ── Откат ─────────────────────────────────────────────────────────────────
# При любом сбое первой установки сервер возвращается в исходное состояние:
# контейнеры, тома, каталог и — если ставили мы — сам Docker. Откат живёт на
# сервере, а не в приложении, именно ради обрыва связи: канал мёртв, а
# убирать за собой всё равно надо.
#
# Повторная установка на уже работающий стек не откатывается никогда: в
# томах данные клиента, и наша неудача не повод их удалять.

set -eu

# Обрыв канала не должен убивать нас на середине отката: писать в закрытый
# stdout мы будем ещё не раз, и SIGPIPE прервал бы уборку.
trap '' PIPE

STACK_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$STACK_DIR"

STATE="$STACK_DIR/.install-state"

# Лог в /tmp, а не в каталоге стека: каталог откат удаляет, а понять, на чём
# всё сломалось, нужно именно после отката. Удачный откат стирает и лог —
# на сервере не остаётся следов. Неудачный оставляет: это единственная
# улика, и она дороже чистоты.
#
# Имя — от mktemp, а не постоянное (Б4 ревизии 2026-09-18). /tmp общий, и
# файл с заранее известным именем любой пользователь сервера мог создать
# сам: тогда root дописывал бы вывод установки в чужой файл, а без
# fs.protected_symlinks — ещё и по чужой ссылке куда угодно. mktemp создаёт
# новый файл с правами 600 и не открывает существующий. Путь уходит
# маркером на шаге preflight, чтобы лог было где найти после отката.
#
# Не создался — останавливаемся сразу, до ловушки: откатывать ещё нечего, а
# без файла лога перенаправления вывода отката и docker ниже падали бы сами.
if ! LOG="$(mktemp /tmp/vpsfocus-install.XXXXXX)"; then
    printf '%s\n' '::step:preflight' '::fail:preflight:logUnavailable'
    exit 1
fi

STEP=""
FAILED=0
FRESH_INSTALL=1
DOCKER_INSTALLED=0
# containerd отдельно от Docker: он бывает на сервере и без docker CLI, и
# тогда он не наш — откат первой установки его не трогает.
CONTAINERD_INSTALLED=0
# Плагин Compose, который поставили мы: Docker на сервере был, а Compose к
# нему — нет (бандл 0.18.0). Откат первой установки его убирает.
COMPOSE_INSTALLED=0
# Чем открыт порт в файрволе: ufw, firewalld или пусто. Откат обязан закрыть
# за собой — оставлять дырку в файрволе клиента после неудачной установки
# нельзя.
FIREWALL_OPENED=""

# Группа пользователя, под которым работает агент в своём контейнере.
#
# Нужна ровно одному файлу — `agent-config/probes.json`: его читает сам
# агент, а не docker, и читает не из-под root. Номер закреплён в образе
# (`agent/Dockerfile`) и в приложении (`stack.rs`), совпадение проверяется
# тестом: разойдись он — ключевые адреса стали бы нечитаемыми, и круг по ним
# молча не пошёл бы ни разу.
AGENT_GID=10001

# Ждём готовности сервисов не бесконечно: оборванная установка должна
# закончиться внятной ошибкой, а не висеть до таймаута SSH.
WAIT_UMAMI_SECONDS=${WAIT_UMAMI_SECONDS:-240}
# Сертификат ждём дольше, чем в 0.1.0: DNS-01 требует, чтобы TXT-запись
# разошлась по DNS, и полторы минуты для этого мало. Истёкшее ожидание
# установку больше не срывает — шаг просто сообщает, что сертификата пока
# нет, и Caddy продолжает попытки сам.
WAIT_TLS_SECONDS=${WAIT_TLS_SECONDS:-150}

# Маркер уходит и в канал, и в лог. Канала может уже не быть — тогда
# остаётся лог; ошибку записи глотаем, иначе set -e оборвёт уборку.
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

write_state() {
    cat > "$STATE" <<STATE_EOF
FRESH_INSTALL=$FRESH_INSTALL
DOCKER_INSTALLED=$DOCKER_INSTALLED
CONTAINERD_INSTALLED=$CONTAINERD_INSTALLED
COMPOSE_INSTALLED=$COMPOSE_INSTALLED
FIREWALL_OPENED=$FIREWALL_OPENED
EDGE_PORT=${EDGE_PORT:-}
STATE_EOF
    chmod 644 "$STATE"
}

# Уборка после сбоя.
#
# Решение «можно ли удалять» принимается здесь, а не в rollback.sh: только
# install.sh знает, стоял ли стек на этом сервере до него.
rollback_now() {
    if [ "$FRESH_INSTALL" != "1" ]; then
        # На сервере работающий стек клиента. Наша неудача не повод трогать
        # его тома — там аналитика за всё время.
        marker "::rollback:skipped"
        marker "::info:${STEP:-preflight}:stackDirLeftForInspection"
        return
    fi

    marker "::rollback:started"

    if [ ! -f "$STACK_DIR/rollback.sh" ]; then
        marker "::rollback:failed"
        return
    fi

    if sh "$STACK_DIR/rollback.sh" >>"$LOG" 2>&1; then
        if [ "$DOCKER_INSTALLED" = "1" ]; then
            marker "::info:rollback:dockerRemoved"
        fi
        marker "::rollback:done"
        # Следов не осталось — включая этот лог.
        rm -f "$LOG" 2>/dev/null || true
    else
        marker "::rollback:failed"
    fi
}

on_exit() {
    status=$?
    # Дальше уборка, и повторный вход в ловушку ей только помешает.
    trap '' EXIT HUP INT TERM PIPE

    if [ "$status" -eq 0 ]; then
        return 0
    fi

    # Упала команда, которую мы не проверили руками: set -e оборвал бы
    # скрипт молча, а приложение обязано узнать, на каком шаге.
    if [ "$FAILED" -eq 0 ]; then
        marker "::fail:${STEP:-preflight}:unexpectedError"
    fi

    rollback_now
}
trap on_exit EXIT

# Прерывание по сигналу — это остановка вручную, а не сбой установки, но
# убрать за собой всё равно надо.
#
# Обрыв связи сюда не приходит вовсе. С версии 0.16.0 скрипт живёт в
# собственной сессии (`setsid`), а его потоки смотрят в файл, а не в канал
# SSH: закрытый канал для него — ничто. До 0.16.0 он жил внутри сессии, и
# обрыв связи убивал его посреди работы — так 2026-09-22 на одноядерном VPS
# установка умерла посреди пакетов Docker, не дойдя ни до отката, ни до
# объяснения.
#
# Выбрасывать почти доделанную установку из-за моргнувшей сети было бы
# дорого и глупо, а сервер в любом случае остаётся в согласованном
# состоянии: либо стек поднят и проверен, либо сбой откатился. Приложение
# узнаёт, что вышло, дочитав журнал прогона.
trap 'FAILED=1; marker "::fail:${STEP:-preflight}:interrupted"; exit 1' HUP INT TERM

has() { command -v "$1" >/dev/null 2>&1; }

# Порт занят кем-то на этом сервере?
port_busy() {
    if has ss; then
        ss -ltnH 2>/dev/null | awk '{print $4}' | grep -qE "[:.]$1\$"
    elif has netstat; then
        netstat -ltn 2>/dev/null | awk 'NR > 2 {print $4}' | grep -qE "[:.]$1\$"
    else
        # Нечем проверить — считаем свободным. Ошибку тогда покажет docker,
        # и она будет понятнее выдуманной нами.
        return 1
    fi
}

# Порт занят нашим же стеком? Тогда это повторный запуск, а не конфликт.
ours() {
    has docker || return 1
    docker info >/dev/null 2>&1 || return 1
    [ -n "$(docker compose ps -q 2>/dev/null || true)" ]
}

# Первое строковое значение ключа в ответе JSON. jq на сервере может не быть,
# а ответы Umami плоские и предсказуемые.
json_field() {
    tr ',' '\n' | grep -m1 "\"$1\":" | sed 's/.*"'"$1"'":"\([^"]*\)".*/\1/'
}

umami_url() { printf 'http://127.0.0.1:%s%s' "$UMAMI_HOST_PORT" "$1"; }

# ── 1. Проверка перед тем, как что-либо менять ────────────────────────────

step preflight

# Где искать лог, если откат не пройдёт (Б4): имя случайное.
info "log:$LOG"

[ "$(id -u)" = "0" ] || die notRoot

for required in docker-compose.yml Caddyfile .env agent.env \
                agent-config/sites.json agent-config/vitals.json \
                agent-config/logs.json agent-config/projects.json \
                agent-config/probes.json; do
    [ -f "$required" ] || die missingFile
done

# .env читается в окружение: docker compose возьмёт его сам, а нам значения
# нужны для проверок и смены пароля админки.
set -a
# shellcheck disable=SC1091
. "$STACK_DIR/.env"
set +a

for name in STACK_NAME STACK_VERSION TRACKING_DOMAIN EDGE_PORT UMAMI_HOST_PORT \
            AGENT_HOST_PORT POSTGRES_PASSWORD UMAMI_APP_SECRET \
            UMAMI_ADMIN_USERNAME UMAMI_ADMIN_PASSWORD; do
    eval "value=\${$name:-}"
    [ -n "$value" ] || die missingVariable
done

# Порт приезжает из приложения и уходит и в правило файрвола, и в docker.
# Проверяем, что это число в допустимом диапазоне: подставленное сюда
# что-то другое собрало бы неверный конфиг там, где ошибка выглядит как
# загадочный отказ docker.
case "$EDGE_PORT" in
    '' | *[!0-9]*) die invalidEdgePort ;;
esac
[ "$EDGE_PORT" -ge 1 ] && [ "$EDGE_PORT" -le 65535 ] || die invalidEdgePort

has curl || die curlMissing

# Первая ли это установка. От ответа зависит, можно ли при сбое удалять тома.
#
# Признак — stack-version.json: его пишет только успешно завершившаяся
# установка. Каталог и даже поднятые контейнеры после сбоя первой установки
# признаком не считаются: они наши, свежие, и удалять их можно.
if [ -f "$STACK_DIR/stack-version.json" ]; then
    FRESH_INSTALL=0
    info existingStackFound
fi
write_state

# Единственный порт, который стек просит наружу, — выбранный при установке.
# На 80 и 443 мы больше не претендуем вовсе: там веб-сервер клиента, и
# отбирать у него порты нельзя ни при каких условиях.
#
# Занят и выбранный? Значит выбирали по устаревшим данным: между проверкой
# и установкой на сервере успели поднять что-то ещё. Отказываем — сказать
# «выберите другой порт» честнее, чем отобрать чужой.
if ! ours; then
    port_busy "$EDGE_PORT" && die edgePortBusy
    port_busy "$UMAMI_HOST_PORT" && die umamiPortBusy
    port_busy "$AGENT_HOST_PORT" && die agentPortBusy
fi

info "edgePort:$EDGE_PORT"

finished

# ── 2. Docker ─────────────────────────────────────────────────────────────

step docker

if has docker; then
    info dockerPresent
else
    info dockerInstalling
    # Официальный скрипт Docker: он же добавляет репозиторий, поэтому дальше
    # обновления приезжают штатным пакетным менеджером сервера.
    curl -fsSL https://get.docker.com -o "$STACK_DIR/get-docker.sh" || die dockerDownloadFailed

    # Отметку ставим до запуска, а не после: скрипт установки успевает
    # поставить пакеты и упасть на последнем шаге, и откат обязан знать, что
    # Docker на сервере появился из-за нас.
    DOCKER_INSTALLED=1
    # containerd смотрим так же, как docker: был до нас — не наш. Без этой
    # отметки откат сносил бы containerd.io и /var/lib/containerd вместе с
    # runtime и образами узла, где containerd стоял без docker CLI.
    if ! has containerd; then
        CONTAINERD_INSTALLED=1
    fi
    write_state

    # Вывод — в лог, а не в /dev/null. Установка Docker — самый долгий и
    # самый ломкий шаг: он тянет пакеты, трогает apt, зависит от версии
    # дистрибутива. До 0.16.0 его вывод уходил целиком в никуда, и наружу
    # выходил один код `dockerInstallFailed` — без причины, навсегда.
    # 2026-09-22 на чистом Ubuntu 24.04 разбирать сбой пришлось по тому,
    # какие файлы остались на диске.
    if ! sh "$STACK_DIR/get-docker.sh" >>"$LOG" 2>&1; then
        rm -f "$STACK_DIR/get-docker.sh"
        die dockerInstallFailed
    fi
    # За собой убираем: установщику Docker в каталоге стека делать нечего.
    rm -f "$STACK_DIR/get-docker.sh"
fi

if ! docker info >/dev/null 2>&1; then
    if has systemctl; then
        systemctl enable --now docker >>"$LOG" 2>&1 || true
    fi
    # Второй раз — уже в лог: этот вызов решает судьбу установки, и «почему
    # демон не отвечает» обязано остаться записанным.
    docker info >>"$LOG" 2>&1 || die dockerDaemonDown
fi

# Docker есть, а Compose к нему нет — ставим плагин сами (бандл 0.18.0).
# До этой версии установка здесь останавливалась, а проверка окружения
# показывала лишь жёлтое предупреждение: проверка обещала больше, чем
# давала установка.
#
# Официальный бинарник с GitHub, версия и суммы закреплены здесь же и
# сверяются до установки: файл кладётся на сервер клиента и выполняется от
# root, а бандл подписан целиком вместе с этими суммами — подмена на
# GitHub или по дороге до сервера не пройдёт. Каталог — системный каталог
# плагинов Docker CLI; пакетный менеджер его не трогает, поэтому снять за
# собой можно одним файлом.
COMPOSE_VERSION="5.5.1"
COMPOSE_SHA256_X86_64="db1889184726840f75c4f9c001048430d4f25b3be3cb084d3ddd762bc0aed576"
COMPOSE_SHA256_AARCH64="732e3a84c1a0f67256ce80bc2598a24546b10ca05f9faa97efceb1171ece2ef7"
COMPOSE_PLUGIN="/usr/local/lib/docker/cli-plugins/docker-compose"

if ! docker compose version >>"$LOG" 2>&1; then
    info composeInstalling
    case "$(uname -m)" in
        x86_64 | amd64) compose_arch="x86_64"; compose_sum="$COMPOSE_SHA256_X86_64" ;;
        aarch64 | arm64) compose_arch="aarch64"; compose_sum="$COMPOSE_SHA256_AARCH64" ;;
        *) die composeMissing ;;
    esac
    has sha256sum || die composeMissing

    compose_tmp="$STACK_DIR/docker-compose.download"
    curl -fsSL "https://github.com/docker/compose/releases/download/v$COMPOSE_VERSION/docker-compose-linux-$compose_arch" \
        -o "$compose_tmp" >>"$LOG" 2>&1 || { rm -f "$compose_tmp"; die composeDownloadFailed; }

    if [ "$(sha256sum "$compose_tmp" | awk '{print $1}')" != "$compose_sum" ]; then
        rm -f "$compose_tmp"
        die composeChecksumMismatch
    fi

    # Отметка — до того, как файл лёг на место: упади установка следующей
    # строкой, откат обязан знать, что плагин появился из-за нас.
    COMPOSE_INSTALLED=1
    write_state
    mkdir -p "$(dirname "$COMPOSE_PLUGIN")"
    mv "$compose_tmp" "$COMPOSE_PLUGIN"
    chmod 755 "$COMPOSE_PLUGIN"
fi

docker compose version >>"$LOG" 2>&1 || die composeMissing

finished

# ── 3. Файрвол ────────────────────────────────────────────────────────────
# Пока стек просил 80 и 443, файрвол трогать было незачем: для уже
# работающего сайта клиента они почти всегда открыты. Выбранный нами порт
# закрыт по умолчанию, и правило приходится добавлять.
#
# Это единственное изменение, которое установка делает за пределами своего
# каталога и Docker. Поэтому:
#
#   * добавляем только разрешающее правило и только для своего порта —
#     ничего не удаляем и не переписываем;
#   * согласие спрашивается в приложении (OPEN_FIREWALL), а dry-run
#     предупреждает об активном файрволе заранее;
#   * откат правило снимает;
#   * nftables и iptables правим не мы. У ufw и firewalld есть
#     декларативный и идемпотентный интерфейс, у сырых цепочек — нет, и
#     вставка правила в чужую цепочку на чужом проде кончается тем, что
#     сайт клиента перестаёт отвечать.
#
# Неудача добавления правила установку не срывает. Docker публикует порты
# через свои цепочки и правило ufw ему, как правило, не требуется вовсе;
# кто на самом деле прав, покажет внешняя проверка достижимости — с нашего
# backend, а не с этого сервера.

step firewall

# Умолчание — «не открывать». Штатный поток всегда пишет переменную в .env
# (приложение спрашивает согласие галочкой), так что умолчание достаётся
# только файлу, собранному руками, — а машине, которая никого не спросила,
# честнее ничего не открывать. Правило файрвола — одно из двух изменений за
# пределами нашего каталога, и делать его молча нельзя.
if [ "${OPEN_FIREWALL:-0}" != "1" ]; then
    info firewallSkipped
elif has ufw && ufw status 2>/dev/null | head -n1 | grep -q 'Status: active'; then
    if ufw status 2>/dev/null | grep -q "^$EDGE_PORT/tcp"; then
        info firewallAlreadyOpen
    elif ufw allow "$EDGE_PORT/tcp" >>"$LOG" 2>&1; then
        FIREWALL_OPENED="ufw"
        write_state
        info "firewallOpened:ufw"
    else
        info "firewallFailed:ufw"
    fi
elif has firewall-cmd && [ "$(firewall-cmd --state 2>/dev/null || true)" = "running" ]; then
    if firewall-cmd --query-port="$EDGE_PORT/tcp" >/dev/null 2>&1; then
        info firewallAlreadyOpen
    elif firewall-cmd --permanent --add-port="$EDGE_PORT/tcp" >>"$LOG" 2>&1 &&
        firewall-cmd --reload >>"$LOG" 2>&1; then
        FIREWALL_OPENED="firewalld"
        write_state
        info "firewallOpened:firewalld"
    else
        info "firewallFailed:firewalld"
    fi
elif has nft || has iptables; then
    info firewallManual
else
    info firewallAbsent
fi

finished

# ── 4. Права на файлы ─────────────────────────────────────────────────────
# Файлы залиты пользователем, под которым мы вошли по SSH. Он не обязан быть
# root, а .env с секретами обязан читаться только им.

step configure

chown -R root:root \
    "$STACK_DIR/docker-compose.yml" "$STACK_DIR/Caddyfile" \
    "$STACK_DIR/.env" "$STACK_DIR/agent.env" "$STACK_DIR/agent-config" \
    2>/dev/null || true
chmod 755 "$STACK_DIR/agent-config"
chmod 644 "$STACK_DIR/docker-compose.yml" "$STACK_DIR/Caddyfile" \
          "$STACK_DIR/agent-config/sites.json" \
          "$STACK_DIR/agent-config/vitals.json" \
          "$STACK_DIR/agent-config/logs.json" \
          "$STACK_DIR/agent-config/projects.json"
# В agent.env пароль от почты и токен бота: те же секреты, что и в .env.
# Их читает docker, а он работает под root, поэтому 600 никому не мешает.
chmod 600 "$STACK_DIR/.env" "$STACK_DIR/agent.env"

# Ключевые адреса — тоже секрет: профиль проверки вправе нести заголовок с
# токеном закрытого раздела клиента. Но права у него не такие, как у .env, и
# разница существенная: этот файл читает сам агент, а он работает в
# контейнере под своим пользователем, а не под root. Права 600 root:root
# означали бы «не читает никто», и круг по ключевым адресам не пошёл бы ни
# разу — молча, потому что файл на месте и выглядит правильным.
#
# Отсюда 640 и группа агента: root пишет, агент читает, остальным на сервере
# файл по-прежнему недоступен. Группа задаётся числом, а не именем: такой
# группы на сервере клиента нет и заводить её мы не станем — она существует
# внутри контейнера, а ядро сверяет номера.
chown root:"$AGENT_GID" "$STACK_DIR/agent-config/probes.json"
chmod 640 "$STACK_DIR/agent-config/probes.json"

# Каталог своего сертификата (правка 1б). Заводим его всегда, даже когда
# сертификат выпускает сам Caddy: иначе при первом запуске его создал бы
# Docker под свою бинд-точку, и права оказались бы не наши. Содержимое
# кладёт приложение — файлами 600, до запуска этого скрипта.
mkdir -p "$STACK_DIR/tls"
chown root:root "$STACK_DIR/tls"
chmod 700 "$STACK_DIR/tls"

# Через if, а не через `[ -f … ] && chmod …`: при отсутствии файла такая
# строка возвращает 1, и set -e обрывает установку на ровном месте.
for script in install.sh rollback.sh; do
    if [ -f "$STACK_DIR/$script" ]; then
        chmod 755 "$STACK_DIR/$script"
    fi
done

finished

# ── 5. Образы и запуск ────────────────────────────────────────────────────

step start

# Только недостающие образы. Отдельным шагом от up, чтобы «не скачалось» и
# «не запустилось» были разными ошибками: первое лечится доступом в сеть,
# второе — нет. Уже скачанное не перекачивается: повторная установка не
# должна тянуть полтора гигабайта заново.
#
# Три попытки, потому что скачивание образов — самое хрупкое место установки.
# Реестры отдают таймаут на ровном месте, и на стенде это ловилось не раз.
# Повтор дёшев: уже скачанные слои остаются, докачивается недостающее.
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

docker compose up -d --remove-orphans || die composeUpFailed

finished

# ── 6. Ожидание готовности ────────────────────────────────────────────────
# «Команда не упала» — не то же самое, что «сервис отвечает». Umami при
# первом запуске накатывает миграции, и это занимает десятки секунд.

step wait

waited=0
healed=0
until curl -fsS -o /dev/null "$(umami_url /api/heartbeat)" 2>/dev/null; do
    waited=$((waited + 3))

    # Сорок пять секунд молчания — повод посмотреть, не висит ли кто в
    # бесконечном перезапуске.
    #
    # Так бывает после неудавшейся установки: она пересоздала сеть проекта,
    # а остановленный контейнер остался в старой. Запустившись, он не видит
    # соседей — Postgres перестаёт разрешаться по имени, — и обычный `up -d`
    # этого не чинит, потому что контейнер с его точки зрения в порядке.
    # Пересоздание чинит. Делаем его только для застрявших сервисов и только
    # один раз: пересоздавать здоровые контейнеры значит ронять аналитику
    # клиента на каждом повторном запуске.
    if [ "$healed" = "0" ] && [ "$waited" -ge 45 ]; then
        healed=1
        # Считаем перезапуски, а не смотрим на состояние: контейнер в цикле
        # падений `docker compose ps` показывает как running — застать его в
        # момент перезапуска почти не выходит.
        stuck=""
        for service in $(docker compose config --services 2>/dev/null); do
            container="$(docker compose ps -aq "$service" 2>/dev/null | head -n1)"
            [ -n "$container" ] || continue
            restarts="$(docker inspect -f '{{.RestartCount}}' "$container" 2>/dev/null || echo 0)"
            if [ "${restarts:-0}" -ge 3 ]; then
                stuck="$stuck $service"
            fi
        done

        if [ -n "$stuck" ]; then
            info recreatingStuck
            # shellcheck disable=SC2086
            docker compose up -d --force-recreate $stuck >>"$LOG" 2>&1 || true
        fi
    fi

    [ "$waited" -lt "$WAIT_UMAMI_SECONDS" ] || die umamiNotReady
    sleep 3
done
info "umamiReadyIn:$waited"

finished

# ── 7. Пароль админки ─────────────────────────────────────────────────────
# Umami создаёт admin/umami при первом запуске. Публичный трекинг-домен с
# дефолтным паролем админки — открытое окно, и закрывать его нужно сразу, а
# не при первом входе пользователя.

step secure

login() {
    curl -fsS -X POST "$(umami_url /api/auth/login)" \
        -H 'Content-Type: application/json' \
        -d "{\"username\":\"$1\",\"password\":\"$2\"}" 2>/dev/null
}

if login "$UMAMI_ADMIN_USERNAME" "$UMAMI_ADMIN_PASSWORD" >/dev/null 2>&1; then
    # Повторный запуск: пароль уже наш, трогать нечего.
    info adminPasswordAlreadySet
else
    response="$(login admin umami || true)"
    [ -n "$response" ] || die adminLoginFailed

    token="$(printf '%s' "$response" | json_field token)"
    user_id="$(printf '%s' "$response" | json_field id)"
    [ -n "$token" ] && [ -n "$user_id" ] || die adminLoginFailed

    curl -fsS -o /dev/null -X POST "$(umami_url "/api/users/$user_id")" \
        -H "Authorization: Bearer $token" \
        -H 'Content-Type: application/json' \
        -d "{\"username\":\"$UMAMI_ADMIN_USERNAME\",\"password\":\"$UMAMI_ADMIN_PASSWORD\"}" \
        || die adminPasswordChangeFailed

    # Проверяем не «запрос прошёл», а «новым паролем действительно входим».
    login "$UMAMI_ADMIN_USERNAME" "$UMAMI_ADMIN_PASSWORD" >/dev/null 2>&1 \
        || die adminPasswordChangeFailed

    # И что старым больше не входим.
    if login admin umami >/dev/null 2>&1; then
        die adminPasswordStillDefault
    fi

    info adminPasswordChanged
fi

finished

# ── 8. Проверка работоспособности ─────────────────────────────────────────

step verify

docker compose exec -T postgres pg_isready -U umami -d umami >>"$LOG" 2>&1 \
    || die postgresNotReady

# Агент отвечает на своей петле. Проверяем именно ответ, а не состояние
# контейнера: поднявшийся и тут же упавший docker ещё секунду показывает
# работающим.
waited=0
until curl -fsS -o /dev/null "http://127.0.0.1:$AGENT_HOST_PORT/health" 2>/dev/null; do
    waited=$((waited + 3))
    [ "$waited" -lt 60 ] || die agentNotReady
    sleep 3
done

# Caddy занял выбранный порт. Проверка нарочно грубая: пока сертификата нет,
# TLS-рукопожатие не состоится, и спросить Caddy по HTTPS нельзя вовсе.
# А вот слушает ли он порт — видно и без сертификата.
waited=0
until port_busy "$EDGE_PORT"; do
    waited=$((waited + 3))
    [ "$waited" -lt 60 ] || die edgeNotListening
    sleep 3
done

finished

# ── 9. Подтверждение домена ───────────────────────────────────────────────
# Сертификат выпускается через ACME DNS-01, а не через порт: занятые 80 и
# 443 нам больше не нужны, но зато нужен TXT-ответ в DNS. DNS расходится
# минутами, иногда десятками минут, и от нашего сервера это не зависит.
#
# Поэтому шаг не блокирующий. Стек к этому моменту уже поднят и проверен;
# всё, чего может не хватать, — сертификата, и Caddy продолжит просить его
# сам, с ретраями, когда установка давно закончится. Приложение показывает
# «стек работает, жду сертификат», а не висящий прогресс-бар.

step domain

# Ходим на сам сервер, подставляя домен: DNS может ещё не разойтись, а
# проверить нужно именно тот виртуальный хост, который отдаст Caddy.
edge() {
    curl -sS -o /dev/null -k --max-time 15 \
        --resolve "$TRACKING_DOMAIN:$EDGE_PORT:127.0.0.1" \
        -w '%{http_code}' "https://$TRACKING_DOMAIN:$EDGE_PORT$1" 2>/dev/null || echo 000
}

CERT_READY=0
waited=0
while [ "$waited" -lt "$WAIT_TLS_SECONDS" ]; do
    if [ "$(edge /script.js)" = "200" ]; then
        CERT_READY=1
        break
    fi
    waited=$((waited + 5))
    sleep 5
done

if [ "$CERT_READY" = "1" ]; then
    info "certificateReadyIn:$waited"

    # Белый список: всё, чего в нём нет, обязано отвечать 404. Проверяем
    # именно админку — ради неё список и заводился.
    #
    # Проверка живёт здесь, а не в предыдущем шаге, по той же причине, что и
    # весь шаг: без сертификата ответа по HTTPS не получить. Если сертификат
    # так и не приехал, список остаётся непроверенным — и тогда его проверит
    # приложение внешним запросом с backend, когда сертификат появится.
    for closed in /login /api/auth/login /api/websites /; do
        code="$(edge "$closed")"
        [ "$code" = "404" ] || die whitelistLeaks
    done
else
    # Не ошибка. Стек работает, сертификата пока нет.
    info certificatePending
fi

finished

# ── 10. Манифест установки ─────────────────────────────────────────────────
# По нему приложение и обновление (спринт 8) узнают, что именно стоит на этом
# сервере, не разбирая docker-compose.yml.

step finish

images="$(docker compose config --images 2>/dev/null | sed 's/.*/"&"/' | paste -sd, - || echo '')"

cat > "$STACK_DIR/stack-version.json" <<JSON
{
  "stackVersion": "$STACK_VERSION",
  "stackName": "$STACK_NAME",
  "installedAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "trackingDomain": "$TRACKING_DOMAIN",
  "edgePort": $EDGE_PORT,
  "certificateReady": $([ "$CERT_READY" = "1" ] && echo true || echo false),
  "umamiHostPort": $UMAMI_HOST_PORT,
  "agentHostPort": $AGENT_HOST_PORT,
  "images": [$images]
}
JSON
chmod 644 "$STACK_DIR/stack-version.json"

# С этой секунды стек не одноразовый: в его томах появятся данные клиента, и
# автоматический откат к ним больше не подойдёт. Ручной — только с --force.
#
# Отметки «Docker и containerd поставили мы» гасим тут же: они нужны только
# откату этой установки, а она удалась. Доживи они до сноса стека через год,
# rollback.sh до правки Б1 (ревизия 2026-09-18) снёс бы вместе со стеком
# Docker клиента со всем, что на нём успело появиться. Нынешний с --force
# Docker не трогает и сам, а флаг, которого нет, не подведёт и следующий.
FRESH_INSTALL=0
DOCKER_INSTALLED=0
CONTAINERD_INSTALLED=0
COMPOSE_INSTALLED=0
write_state

finished

marker "::ready"

# Лог удачной установки не нужен: следующая начнёт свой. Последней строкой,
# после всех маркеров — иначе они создали бы файл заново.
rm -f "$LOG" 2>/dev/null || true
