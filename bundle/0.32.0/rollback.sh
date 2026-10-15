#!/bin/sh
# Откат неудавшейся установки.
#
# Обещание продукта: при сбое установки VPS возвращается в то состояние, в
# котором был. Ни контейнеров, ни томов, ни каталога — и ни Docker, если его
# поставили мы, и ни открытого порта в файрволе, если открывали его мы.
#
# Вызывается из ловушки в install.sh и умеет запускаться отдельно: если
# связь оборвалась посреди установки, откат должен выполниться независимо от
# того, жив ли канал.
#
# ── Чего этот скрипт НЕ делает ────────────────────────────────────────────
#
# Он отказывается трогать стек, который стоял здесь до нас. `docker compose
# down -v` удаляет тома, а в них аналитика клиента за всё время. Повторная
# установка на работающий сервер — обычное дело (её перезапускают после
# обрыва связи), и упади она на середине, откат уничтожил бы данные, к
# которым не имел отношения.
#
# Поэтому решает не этот скрипт, а install.sh: он записывает в
# .install-state, была ли установка первой. Ручной запуск с --force снимает
# защиту — это осознанное действие человека, а не автоматика.
#
# И Docker он с --force не трогает никогда (Б1 ревизии 2026-09-18). --force —
# это снос стека, который мог проработать год, а за год на Docker, который
# когда-то поставили мы, клиент успевает завести свои контейнеры и тома.
# Удалять Docker разрешено только откату первой установки — ветке без
# --force, куда попадают минуты спустя после того, как Docker появился.

set -eu

STACK_DIR="$(cd "$(dirname "$0")" && pwd)"
STATE="$STACK_DIR/.install-state"

FORCE=0
if [ "${1:-}" = "--force" ]; then
    FORCE=1
fi

FRESH_INSTALL=0
DOCKER_INSTALLED=0
CONTAINERD_INSTALLED=0
COMPOSE_INSTALLED=0
FIREWALL_OPENED=""
EDGE_PORT=""
if [ -r "$STATE" ]; then
    # shellcheck disable=SC1090
    . "$STATE"
fi

has() { command -v "$1" >/dev/null 2>&1; }

if [ "$FRESH_INSTALL" != "1" ] && [ "$FORCE" != "1" ]; then
    echo "Откат отменён: стек на этом сервере стоял до установки." >&2
    echo "В его томах данные клиента. Удалить всё вместе с ними: $0 --force" >&2
    exit 2
fi

# ── 1. Контейнеры, сеть и тома ────────────────────────────────────────────
# Без -v тома пережили бы откат, и следующая установка получила бы базу со
# старым паролем — ровно та поломка, от которой откат и защищает.

if has docker && [ -f "$STACK_DIR/docker-compose.yml" ]; then
    cd "$STACK_DIR"
    docker compose down -v --remove-orphans --timeout 30 || true
fi

# ── 2. Docker, если его поставили мы в этой же установке ──────────────────
# Только если поставили мы. Docker, который был на сервере до нас, — рабочий
# инструмент клиента, и сносить его вместе со своей неудачей нельзя.
#
# И только без --force: флаг DOCKER_INSTALLED говорит, кто поставил Docker, но
# не говорит, чьё на нём живёт сегодня. Снос стека через год после установки
# с этим флагом унёс бы контейнеры и тома клиента (Б1). Отсюда отдельная
# ветка, а не флаг: без --force сюда доходит только неудавшаяся первая
# установка (защита выше выходит при FRESH_INSTALL=0).

if [ "$FORCE" != "1" ] && [ "$DOCKER_INSTALLED" = "1" ]; then
    if has systemctl; then
        systemctl disable --now docker docker.socket >/dev/null 2>&1 || true
    fi

    PACKAGES="docker-ce docker-ce-cli docker-ce-rootless-extras docker-buildx-plugin docker-compose-plugin"

    # containerd — отдельно от Docker. Он бывает на сервере и без docker CLI
    # (kubelet, nerdctl), и тогда get.docker.com его не ставит, а берёт
    # готовый. install.sh отмечает, был ли containerd до нас; чужой runtime
    # вместе с его образами откат не трогает.
    if [ "$CONTAINERD_INSTALLED" = "1" ]; then
        if has systemctl; then
            systemctl disable --now containerd >/dev/null 2>&1 || true
        fi
        PACKAGES="$PACKAGES containerd.io"
    fi

    # Вывод не глушим: install.sh зовёт откат с перенаправлением в свой лог,
    # и «почему Docker не удалился» должно остаться записанным. Запущенный
    # руками откат печатает то же самое в терминал человеку.
    if has apt-get; then
        DEBIAN_FRONTEND=noninteractive apt-get purge -y $PACKAGES || true
    elif has dnf; then
        dnf remove -y $PACKAGES || true
    elif has yum; then
        yum remove -y $PACKAGES || true
    fi

    # То, что добавил официальный скрипт установки: репозиторий и ключ.
    rm -f /etc/apt/sources.list.d/docker.list /etc/apt/keyrings/docker.asc \
          /etc/yum.repos.d/docker-ce.repo >/dev/null 2>&1 || true

    # Образы и состояние демона. Клиентских образов здесь быть не может:
    # Docker появился на сервере минуты назад вместе с этой же установкой, и
    # она не закончилась.
    rm -rf /var/lib/docker /etc/docker >/dev/null 2>&1 || true
    if [ "$CONTAINERD_INSTALLED" = "1" ]; then
        rm -rf /var/lib/containerd >/dev/null 2>&1 || true
    fi
fi

# Плагин Compose, если его поставили мы к Docker, который был до нас (бандл
# 0.18.0). По той же причине, что и Docker, — только без --force: через год
# этим Compose пользуются и чужие контейнеры клиента.
if [ "$FORCE" != "1" ] && [ "$COMPOSE_INSTALLED" = "1" ]; then
    rm -f /usr/local/lib/docker/cli-plugins/docker-compose || true
fi

# ── 3. Правило файрвола, если его добавили мы ─────────────────────────────
# Открытый порт — изменение на сервере клиента, и после отката его быть не
# должно. Снимаем только то правило, которое добавили сами: чем открыт порт
# и какой, записано в .install-state. Правило, которое было у клиента до нас
# (порт совпал случайно), install.sh не отмечает — и мы его не тронем.

if [ -n "$FIREWALL_OPENED" ] && [ -n "$EDGE_PORT" ]; then
    case "$FIREWALL_OPENED" in
        ufw)
            ufw delete allow "$EDGE_PORT/tcp" || true
            ;;
        firewalld)
            firewall-cmd --permanent --remove-port="$EDGE_PORT/tcp" || true
            firewall-cmd --reload || true
            ;;
    esac
fi

# ── 4. Каталог стека ──────────────────────────────────────────────────────
# Последним: пока он на месте, откат можно повторить.

rm -rf "$STACK_DIR"

echo "Откат завершён: сервер возвращён в исходное состояние."
