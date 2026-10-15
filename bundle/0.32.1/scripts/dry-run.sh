#!/bin/sh
# Проверка готовности сервера. ТОЛЬКО ЧТЕНИЕ.
#
# Скрипт не устанавливает, не изменяет и не создаёт ничего — включая самого
# себя: приложение отдаёт его на stdin (`sh -s`), поэтому на диск сервера он
# не попадает. Это обещание пользователю, а не деталь реализации: агентство
# запускает нас на чужом проде.
#
# Написан на POSIX sh, а не на bash, намеренно. Наличие bash — один из
# проверяемых фактов, и проверка не должна падать там, где его нет.
#
# Вывод — одна строка JSON. Никакой другой печати в stdout быть не должно,
# иначе разбор на стороне приложения сломается.
#
# Живёт в общей части установочного бандла: попадает в каждую его версию и
# этой же копией вшивается в приложение через include_str!. Проверка обязана
# работать до входа в аккаунт и без сети, поэтому вшитая копия остаётся
# всегда. Если бандл скачан — берётся скрипт из него, и тогда проверку можно
# чинить, не перевыпуская приложение.

set -u

# ── Помощники вывода ──────────────────────────────────────────────────────

# Экранирует строку для JSON и убирает управляющие символы: значения здесь
# однострочные, а сырой перевод строки сломал бы разбор.
esc() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\000-\037'
}

str() { printf '"%s":"%s",' "$1" "$(esc "$2")"; }
num() { printf '"%s":%s,' "$1" "${2:-0}"; }
bol() { printf '"%s":%s,' "$1" "$2"; }

has() { command -v "$1" >/dev/null 2>&1; }

# Команда, которой нужны права. Файрвол под непривилегированным
# пользователем не читается: ufw и firewall-cmd отвечают отказом, а не
# состоянием. Пароля не спрашиваем — `sudo -n` либо проходит, либо нет.
#
# Это по-прежнему только чтение: ни одна из вызываемых здесь команд ничего
# не меняет.
PRIV=""
if [ "$(id -u 2>/dev/null || echo 1000)" != "0" ] && has sudo && sudo -n true 2>/dev/null; then
    PRIV="sudo -n"
fi
priv() { $PRIV "$@" 2>/dev/null; }

# ── Операционная система ──────────────────────────────────────────────────

os_id=""
os_version=""
os_pretty=""
if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release 2>/dev/null || true
    os_id="${ID:-}"
    os_version="${VERSION_ID:-}"
    os_pretty="${PRETTY_NAME:-}"
fi
[ -n "$os_pretty" ] || os_pretty="$(uname -s 2>/dev/null) $(uname -r 2>/dev/null)"

kernel="$(uname -r 2>/dev/null || echo '')"

# ── Архитектура ───────────────────────────────────────────────────────────
# ARM-серверы обычны у Hetzner и Oracle Cloud. Если не свести имена к
# amd64/arm64 здесь, несовпадение всплывёт на запуске контейнеров.

raw_arch="$(uname -m 2>/dev/null || echo unknown)"
case "$raw_arch" in
    x86_64 | amd64) arch="amd64" ;;
    aarch64 | arm64) arch="arm64" ;;
    *) arch="$raw_arch" ;;
esac

# ── Docker ────────────────────────────────────────────────────────────────

docker_present=false
docker_version=""
docker_daemon=false
compose_version=""

if has docker; then
    docker_present=true
    docker_version="$(docker --version 2>/dev/null | head -n1)"
    # docker info без прав вернёт ошибку доступа, а не «демон мёртв».
    # Поэтому второй раз спрашиваем с теми же правами, с какими пойдёт
    # установка (`priv` — sudo -n, если он есть без пароля): пользователь с
    # sudo, но не в группе docker, иначе получал блокер «Docker не работает»,
    # хотя установка у него прошла бы (найдено 2026-09-25). docker info
    # только читает — правило «проверка ничего не меняет» в силе.
    if docker info >/dev/null 2>&1 || priv docker info >/dev/null; then
        docker_daemon=true
    fi
    compose_version="$(docker compose version --short 2>/dev/null || echo '')"
fi

# ── Память ────────────────────────────────────────────────────────────────

mem_total_mb=0
mem_available_mb=0
if [ -r /proc/meminfo ]; then
    mem_total_mb="$(awk '/^MemTotal:/ {printf "%d", $2 / 1024; exit}' /proc/meminfo)"
    mem_available_mb="$(awk '/^MemAvailable:/ {printf "%d", $2 / 1024; exit}' /proc/meminfo)"
fi
[ -n "$mem_total_mb" ] || mem_total_mb=0
[ -n "$mem_available_mb" ] || mem_available_mb=0

# ── Диск ──────────────────────────────────────────────────────────────────
# df -k есть везде; -m поддерживают не все реализации.

disk_free_mb="$(df -Pk / 2>/dev/null | awk 'NR == 2 {printf "%d", $4 / 1024; exit}')"
[ -n "$disk_free_mb" ] || disk_free_mb=0

# ── Занятые порты ─────────────────────────────────────────────────────────
# 80 и 443 больше не требуются: Caddy слушает порт, выбранный при установке
# (решение 2026-08-28), а сертификат берётся через DNS-01. Занятость этих
# двух портов остаётся фактом — по нему видно, что на сервере уже есть
# веб-сервер клиента, — но блокером она быть перестала.
#
# Вместо этого собираем, какие из портов, совместимых с проксированием
# Cloudflare, свободны. Выбор порта делает приложение: скрипт по-прежнему
# только сообщает факты.

listening=""
if has ss; then
    listening="$(ss -ltnH 2>/dev/null | awk '{print $4}')"
elif has netstat; then
    listening="$(netstat -ltn 2>/dev/null | awk 'NR > 2 {print $4}')"
fi

busy() {
    printf '%s\n' "$listening" | grep -qE "[:.]$1\$"
}

port_busy() {
    busy "$1" && echo true || echo false
}

port_80=$(port_busy 80)
port_443=$(port_busy 443)

# Порты, которые Cloudflare проксирует на бесплатном плане. Агентство часто
# держит DNS там с включённым проксированием, и произвольный порт оно не
# пропустит.
edge_free=""
for candidate in 443 2053 2083 2087 2096 8443; do
    busy "$candidate" || edge_free="${edge_free:+$edge_free,}$candidate"
done

# Если заняты все — берём первый свободный из своего запасного списка.
# Порт вне списка Cloudflare работает, но проксирование через него не
# пройдёт; предупредить об этом — дело интерфейса, а не скрипта.
edge_fallback=0
for candidate in 18443 28443 38443 48443 58443; do
    if ! busy "$candidate"; then
        edge_fallback="$candidate"
        break
    fi
done

# ── Файрвол ───────────────────────────────────────────────────────────────
# Пока Caddy просил 80 и 443, файрвол проверять было незачем: для уже
# работающего сайта клиента они почти всегда открыты. Для выбранного нами
# порта это перестаёт быть правдой — установке придётся добавить правило, а
# пользователь обязан знать об этом заранее: мы на чужом проде.

firewall=""
firewall_active=false
firewall_readable=false

if has ufw; then
    firewall="ufw"
    state="$(priv ufw status | head -n1)"
    case "$state" in
        *active*)
            firewall_readable=true
            # «inactive» тоже содержит «active» — сначала отсекаем его.
            case "$state" in
                *inactive*) : ;;
                *) firewall_active=true ;;
            esac
            ;;
    esac
elif has firewall-cmd; then
    firewall="firewalld"
    state="$(priv firewall-cmd --state)"
    case "$state" in
        running)
            firewall_readable=true
            firewall_active=true
            ;;
        not\ running)
            firewall_readable=true
            ;;
    esac
elif has nft; then
    firewall="nftables"
    # Читаемость определяется кодом возврата самой nft, а не выводом grep.
    # `grep -c` печатает число всегда, в том числе когда читать было нечего,
    # поэтому проверка «вывод непустой» была истинной при любом исходе:
    # нечитаемый файрвол объявлялся «есть, но выключен», и установка после
    # этого не предлагала открыть порт.
    if ruleset="$(priv nft list ruleset)"; then
        firewall_readable=true
        if printf '%s\n' "$ruleset" | grep -q 'type filter hook input'; then
            firewall_active=true
        fi
    fi
elif has iptables; then
    firewall="iptables"
    if rules="$(priv iptables -S INPUT)"; then
        firewall_readable=true
        if printf '%s\n' "$rules" | grep -qE '^-A INPUT|^-P INPUT (DROP|REJECT)'; then
            firewall_active=true
        fi
    fi
fi

# ── Маршрутизация ─────────────────────────────────────────────────────────
# Docker, которого на сервере нет, поставит установка — и хозяином iptables
# станет он: заведёт свои цепочки в `nat` и `filter` и поставит политику
# цепочки FORWARD в DROP. На сервере, который что-то маршрутизирует —
# WireGuard до офиса, LXC, второй интерфейс, — форвардинг ломается в момент
# установки Docker (Б7 ревизии 2026-09-18).
#
# Собираем два признака такого сервера; оценивает их приложение, и только
# там, где Docker ещё не стоит: где он стоит, эти правила — его же.

ip_forward=false
[ "$(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo 0)" = "1" ] && ip_forward=true

# Ищем не «есть ли цепочка FORWARD» — она есть на любом сервере, где
# загружен iptables, — а правила, которые маршрутизируют чужой трафик: NAT
# наружу и проброс портов. Их пишут руками или демоны вроде WireGuard, libvirt
# и LXC. Предупреждение, которое видят все, не читает никто.
forward_rules=false

if has nft && ruleset="$(priv nft list ruleset)"; then
    printf '%s\n' "$ruleset" | grep -qE '(masquerade|snat|dnat)' && forward_rules=true
fi

if [ "$forward_rules" = "false" ] && has iptables; then
    priv iptables -t nat -S |
        grep -qE '^-A .* -j (MASQUERADE|SNAT|DNAT|REDIRECT)' && forward_rules=true
fi

# И правила в самой FORWARD — кроме каркаса файрвола. ufw, fail2ban, libvirt
# и Docker заводят там свои переходы на любом сервере; политики цепочек
# (`-P`) не в счёт по той же причине.
if [ "$forward_rules" = "false" ] && has iptables; then
    priv iptables -S FORWARD | grep '^-A FORWARD' |
        grep -qvE ' -j (ufw|f2b|LIBVIRT|DOCKER)' && forward_rules=true
fi

# ── Права ─────────────────────────────────────────────────────────────────

uid="$(id -u 2>/dev/null || echo 1000)"
is_root=false
[ "$uid" = "0" ] && is_root=true

sudo_present=false
sudo_nopasswd=false
if has sudo; then
    sudo_present=true
    # -n: не спрашивать пароль. Интерактивный запрос повесил бы сессию.
    if sudo -n true 2>/dev/null; then
        sudo_nopasswd=true
    fi
fi

# ── Прочее, что нужно установщику ─────────────────────────────────────────

systemd=false
[ -d /run/systemd/system ] && systemd=true

bash_present=false
has bash && bash_present=true

curl_present=false
has curl && curl_present=true

# ── Сборка ответа ─────────────────────────────────────────────────────────

printf '{'
str "osId" "$os_id"
str "osVersion" "$os_version"
str "osPretty" "$os_pretty"
str "kernel" "$kernel"
str "arch" "$arch"
str "rawArch" "$raw_arch"
bol "dockerPresent" "$docker_present"
str "dockerVersion" "$docker_version"
bol "dockerDaemon" "$docker_daemon"
str "composeVersion" "$compose_version"
num "memTotalMb" "$mem_total_mb"
num "memAvailableMb" "$mem_available_mb"
num "diskFreeMb" "$disk_free_mb"
bol "port80Busy" "$port_80"
bol "port443Busy" "$port_443"
str "edgePortsFree" "$edge_free"
num "edgePortFallback" "$edge_fallback"
str "firewall" "$firewall"
bol "firewallActive" "$firewall_active"
bol "firewallReadable" "$firewall_readable"
bol "ipForward" "$ip_forward"
bol "forwardRules" "$forward_rules"
bol "isRoot" "$is_root"
bol "sudoPresent" "$sudo_present"
bol "sudoNopasswd" "$sudo_nopasswd"
bol "systemd" "$systemd"
bol "bashPresent" "$bash_present"
# Последнее поле без запятой.
printf '"curlPresent":%s}' "$curl_present"
printf '\n'
