#!/bin/sh
# Проверка безопасности сервера. ТОЛЬКО ЧТЕНИЕ.
#
# Скрипт ничего не устанавливает, не изменяет и не создаёт — включая самого
# себя: приложение отдаёт его на stdin (`sh -s`), на диск сервера он не
# попадает. Это обещание человеку, а не деталь реализации: мы на чужом проде.
#
# «Только чтение» здесь буквально:
#   * списки пакетов не обновляются (`apt-get update` пишет на диск) — число
#     обновлений берётся из того, что система уже знает, прогоном без
#     изменений и без замка (`apt-get -s`);
#   * dnf не запускается вовсе: каждый его вызов оставляет запись в своём
#     журнале, даже запрос из кеша. Про обновления в семействе RHEL скрипт
#     честно отвечает «не знаю»;
#   * пароль не спрашивается никогда: `sudo -n` либо проходит, либо нет.
#
# Скрипт собирает ФАКТЫ и ничего не оценивает: что красное, а что жёлтое,
# решает приложение (`servercheck.rs`). Факт, который узнать не удалось,
# уезжает пустой строкой или -1 — «не знаем», а не «в порядке».
#
# POSIX sh, не bash. Вывод — одна строка JSON, другой печати в stdout нет.
#
# Вшит в приложение, а не в установочный бандл: `dry-run.sh` входит в
# подписанную опись каждой версии бандла, и править его значило бы
# переподписывать их все. Мониторинг для этой проверки не нужен вовсе.

set -u

# Корень файловой системы. Пуст всегда, кроме тестов: они подставляют сюда
# временный каталог, чтобы проверить чтение отметок без настоящего сервера.
ROOT="${VPSFOCUS_CHECK_ROOT:-}"

# ── Помощники ─────────────────────────────────────────────────────────────

esc() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\000-\037'
}

str() { printf '"%s":"%s",' "$1" "$(esc "$2")"; }
num() { printf '"%s":%s,' "$1" "${2:-0}"; }
bol() { printf '"%s":%s,' "$1" "$2"; }

has() { command -v "$1" >/dev/null 2>&1; }

# Права на чтение. Настройки sshd, имена процессов у чужих сокетов, журнал
# входов и контейнеры Docker обычному пользователю не видны.
privileged=false
PRIV=""
if [ "$(id -u 2>/dev/null || echo 1000)" = "0" ]; then
    privileged=true
elif has sudo && sudo -n true 2>/dev/null; then
    privileged=true
    PRIV="sudo -n"
fi
priv() { $PRIV "$@" 2>/dev/null; }

# Потолок времени на медленные команды: сервер чужой и бывает одноядерным,
# а журнал входов за сутки — большим.
LIMIT=""
has timeout && LIMIT="timeout 20"

# ── Система ───────────────────────────────────────────────────────────────

os_pretty=""
if [ -r "$ROOT/etc/os-release" ]; then
    # shellcheck disable=SC1090,SC1091
    . "$ROOT/etc/os-release" 2>/dev/null || true
    os_pretty="${PRETTY_NAME:-}"
fi
[ -n "$os_pretty" ] || os_pretty="$(uname -s 2>/dev/null) $(uname -r 2>/dev/null)"

# ── Вход по SSH ───────────────────────────────────────────────────────────
# `sshd -T` печатает итоговые настройки — после всех Include и умолчаний.
# Читать sshd_config самим нельзя: умолчания у версий разные, и «строки нет»
# значит разное. Команда читает ключи хоста и без прав отказывает.

sshd_bin=""
if has sshd; then
    sshd_bin="sshd"
elif [ -x /usr/sbin/sshd ]; then
    sshd_bin="/usr/sbin/sshd"
fi

ssh_password=""
ssh_root=""
if [ -n "$sshd_bin" ] && conf="$(priv "$sshd_bin" -T)"; then
    ssh_password="$(printf '%s\n' "$conf" |
        awk 'tolower($1) == "passwordauthentication" { print tolower($2); exit }')"
    ssh_root="$(printf '%s\n' "$conf" |
        awk 'tolower($1) == "permitrootlogin" { print tolower($2); exit }')"
fi

# ── Что слушает на сервере ────────────────────────────────────────────────
# Адрес и имя процесса каждого слушающего TCP-порта: «адрес|процесс;…».
# Имена процессов видны только с правами; без них остаются адреса и порты.

listeners=""
listeners_known=false
if has ss; then
    if raw="$(priv ss -ltnpH)" && [ -n "$raw" ]; then
        listeners_known=true
    elif raw="$(ss -ltnH 2>/dev/null)"; then
        listeners_known=true
    fi
    if [ "$listeners_known" = "true" ]; then
        listeners="$(printf '%s\n' "$raw" | awk '
            NF >= 4 {
                proc = ""
                if (match($0, /users:\(\("[^"]+"/)) {
                    proc = substr($0, RSTART + 9, RLENGTH - 10)
                }
                printf "%s|%s;", $4, proc
            }')"
    fi
elif has netstat; then
    if raw="$(priv netstat -ltnp)" && [ -n "$raw" ]; then
        listeners_known=true
        listeners="$(printf '%s\n' "$raw" | awk '
            $1 ~ /^tcp/ {
                proc = $7
                sub(/^[0-9]+\//, "", proc)
                if (proc == "-") proc = ""
                printf "%s|%s;", $4, proc
            }')"
    fi
fi

# ── Порты, опубликованные Docker ──────────────────────────────────────────
# Docker публикует порт своими правилами iptables, мимо ufw: файрвол
# включён, порт в нём закрыт — а снаружи он открыт. «имя|порты;…».

docker_state="none"
docker_ports=""
if has docker; then
    if out="$(priv docker ps --format '{{.Names}}|{{.Ports}}')"; then
        docker_state="ok"
        docker_ports="$(printf '%s\n' "$out" | tr '\n' ';')"
    else
        docker_state="unreadable"
    fi
fi

# ── Файрвол ───────────────────────────────────────────────────────────────
# Разбор тот же, что в dry-run.sh бандла, кроме ufw: тот скрипт подписан
# вместе с бандлом и править его нельзя — копия здесь.
#
# ufw читается из его настроек, а не командой `ufw status`: та берёт замок и
# оставляет /run/ufw.lock (найдено прогоном в контейнере Ubuntu 24.04), а
# файлов на сервере мы не оставляем. `ENABLED=yes` пишет `ufw enable` — это
# и есть «файрвол включён».

firewall=""
firewall_active=false
firewall_readable=false

if has ufw; then
    firewall="ufw"
    if conf="$(priv cat "$ROOT/etc/ufw/ufw.conf")" && [ -n "$conf" ]; then
        firewall_readable=true
        if printf '%s\n' "$conf" | grep -qiE '^[[:space:]]*ENABLED[[:space:]]*=[[:space:]]*"?yes'; then
            firewall_active=true
        fi
    fi
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

# ── Обновления безопасности ───────────────────────────────────────────────
# -1 — не знаем. `apt-get -s` — прогон без изменений; `NoLocking` — чтобы
# не трогать и замок. `dist-upgrade`, а не `upgrade`: обновление ядра тянет
# новый пакет, и `upgrade` его молча придерживает.

pkg=""
updates_total=-1
updates_security=-1
updates_stamp=0
if has apt-get; then
    pkg="apt"
    # Planner — в /dev/null: даже прогон без изменений пишет протокол
    # решателя в /var/log/apt/eipp.log.xz (найдено прогоном в контейнере
    # Ubuntu 24.04), а файлов на сервере мы не оставляем.
    if sim="$($LIMIT apt-get -s -o Debug::NoLocking=true -o Dir::Log::Planner=/dev/null         dist-upgrade 2>/dev/null)"; then
        # Только обновления установленного: у них в скобках стоит старая
        # версия (`Inst vim [9.1] (9.2 …)`). Без скобок — новый пакет, который
        # `dist-upgrade` ставит зависимостью; он не обновление и не закрывает
        # уязвимость. На живом Ubuntu 24.04 (2026-10-08) таких было 34 из 213,
        # и единственная строка из -security была как раз новым пакетом.
        upgrades="$(printf '%s\n' "$sim" | grep '^Inst [^ ]* \[')"
        updates_total="$(printf '%s\n' "$upgrades" | grep -c '^Inst ')"
        updates_security="$(printf '%s\n' "$upgrades" | grep -ci 'security')"
    fi
    # Когда сервер в последний раз обновлял списки пакетов: число выше
    # верно на этот день, а не на сегодня.
    for stamp in /var/lib/apt/periodic/update-success-stamp /var/lib/apt/lists; do
        if [ -e "$ROOT$stamp" ]; then
            updates_stamp="$(stat -c %Y "$ROOT$stamp" 2>/dev/null || echo 0)"
            break
        fi
    done
elif has dnf || has yum; then
    pkg="dnf"
fi

# ── Ждёт ли сервер перезагрузки ───────────────────────────────────────────
# Debian и Ubuntu ставят отметку — но только там, где есть тот, кто её
# ставит: без него «отметки нет» не значит «перезагрузка не нужна».
# Иначе сравниваем работающее ядро с самым новым установленным.

reboot=""
if [ -e "$ROOT/var/run/reboot-required" ]; then
    reboot="yes"
elif [ -e "$ROOT/etc/kernel/postinst.d/update-notifier" ] ||
    [ -e "$ROOT/etc/kernel/postinst.d/unattended-upgrades" ]; then
    reboot="no"
elif has sort && [ -d "$ROOT/lib/modules" ]; then
    # Самое новое установленное ядро — по каталогам модулей: это только
    # чтение списка. Не `rpm -q`: он открывает свою базу и трогает её
    # служебный файл (rpmdb.sqlite-shm, найдено прогоном в Rocky Linux 9).
    #
    # Только если работающее ядро есть среди установленных. На VPS в
    # контейнере (OpenVZ, LXC) ядро — хоста, а в /lib/modules лежат пакеты
    # дистрибутива, которые не запускались никогда: сравнение с ними
    # объявляло бы «ждёт перезагрузки» навсегда.
    running="$(uname -r 2>/dev/null)"
    newest="$(ls -1 "$ROOT/lib/modules" 2>/dev/null | sort -V 2>/dev/null | tail -n1)"
    if [ -n "$newest" ] && [ -n "$running" ] && [ -d "$ROOT/lib/modules/$running" ]; then
        if [ "$newest" = "$running" ]; then
            reboot="no"
        else
            reboot="yes"
        fi
    fi
fi

# ── Подбор пароля ─────────────────────────────────────────────────────────
# Сколько раз за сутки пароль не подошёл. Только с правами: без них журнал
# отвечает пустотой, и ноль был бы неправдой. Строки считаются на лету, в
# память журнал не читается.

auth_failures=-1
if [ "$privileged" = "true" ] && has journalctl; then
    auth_failures="$(priv $LIMIT journalctl -u ssh -u sshd --since '-24h' --no-pager -q -o cat |
        grep -c 'Failed password')"
    [ -n "$auth_failures" ] || auth_failures=-1
fi

# ── Защита от подбора ─────────────────────────────────────────────────────

guard=""
guard_known=false
if has systemctl; then
    guard_known=true
    for name in fail2ban crowdsec sshguard; do
        if systemctl is-active --quiet "$name" 2>/dev/null; then
            guard="$name"
            break
        fi
    done
fi
if [ -z "$guard" ] && has pgrep; then
    guard_known=true
    for name in fail2ban-server crowdsec sshguard; do
        if pgrep -x "$name" >/dev/null 2>&1; then
            guard="${name%-server}"
            break
        fi
    done
fi

# ── Время (трек Z, «Требования к сайту») ─────────────────────────────────
# Служба времени и её отметка о синхронизации. Только то, что читается без
# побочных действий: systemd-timesyncd сам кладёт отметку
# /run/systemd/timesync/synchronized, когда сверился. `timedatectl` не
# зовём — он будит службу timedated, и та пишет в журнал о своём запуске;
# `chronyc` — он создаёт на время свой сокет в /run/chrony. Про chrony и ntpd
# поэтому известно только «служба работает».
#
# VPS в контейнере (OpenVZ, LXC) берёт время у хоста: своей службы ему не
# нужно, и «службы нет» там не находка.

time_service=""
time_synced=""
time_container=false
time_known=false
if has systemctl || has pgrep; then
    time_known=true
fi
if has systemd-detect-virt; then
    if systemd-detect-virt --container --quiet 2>/dev/null; then
        time_container=true
    fi
# Без systemd — по отметкам, которые кладут сами контейнеры: Docker,
# Podman и OpenVZ (у его гостя есть /proc/vz, а /proc/bc — только у хоста).
elif [ -e "$ROOT/.dockerenv" ] || [ -e "$ROOT/run/.containerenv" ] ||
    { [ -d "$ROOT/proc/vz" ] && [ ! -d "$ROOT/proc/bc" ]; }; then
    time_container=true
fi
if has systemctl; then
    for name in systemd-timesyncd chrony chronyd ntp ntpd openntpd; do
        if systemctl is-active --quiet "$name" 2>/dev/null; then
            time_service="$name"
            break
        fi
    done
fi
if [ -z "$time_service" ] && has pgrep; then
    for name in systemd-timesyncd chronyd ntpd openntpd; do
        if pgrep -x "$name" >/dev/null 2>&1; then
            time_service="$name"
            break
        fi
    done
fi
# Отметку timesyncd кладёт с systemd 239; у старшего её нет никогда, и
# «нет отметки» там значило бы ложное «не сверилось» — тогда не знаем.
if [ "$time_service" = "systemd-timesyncd" ]; then
    systemd_version="$(systemctl --version 2>/dev/null | awk 'NR == 1 { print $2 + 0 }')"
    if [ -e "$ROOT/run/systemd/timesync/synchronized" ]; then
        time_synced="yes"
    elif [ "${systemd_version:-0}" -ge 239 ] 2>/dev/null; then
        time_synced="no"
    fi
fi

# ── Срок хранения логов веб-сервера ───────────────────────────────────────
# В логах — адреса посетителей, а это персональные данные: хранить их
# дольше нужного нельзя. Срок задаёт logrotate: «имя|rotate|период|maxage;»,
# пустое поле — строки нет. Первой строкой — общие умолчания
# /etc/logrotate.conf: им подчиняется файл, где своих строк нет. Файлы
# только читаются; `logrotate -d` не зовём — он открывает файл состояния.

log_rotate=""
# Первое значение каждой строки. `top=1` — только вне блоков `{ … }`: в
# logrotate.conf старых Debian после общих строк идут блоки wtmp и btmp со
# своими `monthly` и `rotate 1`, и общими они не являются. Ротация по
# размеру без периода — период `size`: срока из неё не вывести.
logrotate_fields() {
    awk -v top="$2" '
        { line = $0; sub(/#.*/, "", line); n = split(line, word, /[ \t]+/); key = (word[1] == "" ? word[2] : word[1]); arg = (word[1] == "" ? word[3] : word[2]) }
        top != 1 || depth == 0 {
            if (key == "rotate" && rotate == "") rotate = arg
            if (key == "maxage" && maxage == "") maxage = arg
            if (key ~ /^(daily|weekly|monthly|yearly)$/ && period == "") period = key
            if (key ~ /^(size|maxsize)$/) size = 1
        }
        { depth += gsub(/\{/, "{", line) - gsub(/\}/, "}", line) }
        END {
            if (period == "" && size) period = "size"
            printf "%s|%s|%s", rotate, period, maxage
        }' "$1" 2>/dev/null
}
if [ -r "$ROOT/etc/logrotate.conf" ]; then
    log_rotate="default|$(logrotate_fields "$ROOT/etc/logrotate.conf" 1);"
fi
for name in nginx apache2 httpd; do
    if [ -r "$ROOT/etc/logrotate.d/$name" ]; then
        log_rotate="$log_rotate$name|$(logrotate_fields "$ROOT/etc/logrotate.d/$name" 0);"
    fi
done

# ── Контейнеры с лишними правами ──────────────────────────────────────────
# `privileged` и проброшенный сокет Docker дают контейнеру весь сервер.
# Тот же `docker` с теми же правами, что `docker ps` выше. «имя|privileged|
# сокет;», сокет — yes или пусто.

docker_risky=""
if [ "$docker_state" = "ok" ] && ids="$(priv docker ps -q)" && [ -n "$ids" ]; then
    # shellcheck disable=SC2086
    docker_risky="$(priv docker inspect --format \
        '{{.Name}}|{{.HostConfig.Privileged}}|{{range .Mounts}}{{if eq .Source "/var/run/docker.sock" "/run/docker.sock"}}yes{{end}}{{end}}' \
        $ids | sed 's#^/##' | tr '\n' ';')"
fi

# ── Свежесть копий сайтов ────────────────────────────────────────────────
# Папки, которые человек сам указал в профиле сайта на экране «Требования к
# сайту» (BACKUP_PATHS, через двоеточие; приложение отбирает символы). Где
# копии, знает только он: у хостера они бывают снимками, которых отсюда не
# видно. «путь|время самого нового файла;», время -1 — папки нет, 0 — в ней
# нет файлов, пусто — прочесть не дали. `find` только читает каталоги.

BACKUP_PATHS="${BACKUP_PATHS:-}"
backups=""
if [ -n "$BACKUP_PATHS" ]; then
    # Без подстановки имён: путь отобран приложением, но * в нём всё равно
    # не должна стать списком файлов.
    set -f
    old_ifs="$IFS"
    IFS=':'
    for path in $BACKUP_PATHS; do
        IFS="$old_ifs"
        [ -n "$path" ] || continue
        # «Папки нет» — только когда её не видно и с правами: без них
        # закрытая папка выглядит так же, как отсутствующая.
        if ! priv test -e "$ROOT$path"; then
            if [ "$privileged" = "true" ] && [ ! -e "$ROOT$path" ]; then
                newest="-1"
            else
                newest=""
            fi
        elif listing="$(priv $LIMIT find "$ROOT$path" -maxdepth 4 -type f -printf '%T@\n')"; then
            newest="$(printf '%s\n' "$listing" | awk -F. 'NF { if ($1 > max) max = $1 } END { print max + 0 }')"
        else
            newest=""
        fi
        backups="$backups$path|$newest;"
        IFS=':'
    done
    IFS="$old_ifs"
    set +f
fi

# ── Сборка ответа ─────────────────────────────────────────────────────────

printf '{'
str "osPretty" "$os_pretty"
bol "privileged" "$privileged"
str "sshPassword" "$ssh_password"
str "sshRoot" "$ssh_root"
bol "listenersKnown" "$listeners_known"
str "listeners" "$listeners"
str "dockerState" "$docker_state"
str "dockerPorts" "$docker_ports"
str "firewall" "$firewall"
bol "firewallActive" "$firewall_active"
bol "firewallReadable" "$firewall_readable"
str "pkg" "$pkg"
num "updatesTotal" "$updates_total"
num "updatesSecurity" "$updates_security"
num "updatesStamp" "$updates_stamp"
str "reboot" "$reboot"
num "authFailures" "$auth_failures"
str "guard" "$guard"
bol "guardKnown" "$guard_known"
str "timeService" "$time_service"
str "timeSynced" "$time_synced"
bol "timeContainer" "$time_container"
bol "timeKnown" "$time_known"
str "logRotate" "$log_rotate"
str "dockerRisky" "$docker_risky"
str "backups" "$backups"
printf '"v":1}\n'
