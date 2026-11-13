#!/bin/sh
# Что занимает диск сервера (трек AH, предложение 2, спринт 257). ТОЛЬКО ЧТЕНИЕ.
#
# По кнопке на «Здоровье сервера» (Р11): разбор — это минута работы диска
# чужой машины, при открытии экрана его не делаем. Скрипт отдаётся на stdin
# (`sh -s`) и на диск сервера не попадает. Команды проверены `find / -xdev
# -newer` в контейнерах ubuntu:24.04 и debian:12 от root, через `sudo -n` и
# без прав (спринт 255): следов от них нет, а то, что оставляет сам `sudo`
# (строка в журнале, `/run/sudo/ts`), — как у прежних скриптов (Р19).
# Поэтому под root `sudo` не зовётся, а через него идут три команды, не
# больше. Новая команда сюда — только после такого же прогона.
#
# Раздел — тот, что мерит агент: корень сервера (`statfs` на `/host/root`).
# `du -x` не уходит на другие разделы (П21): они перечисляются отдельно по
# `df`, без разбора.
#
# Факты, а не оценки: что можно чистить, решает экран. Не успели — признак
# `timedOut`, а не сумма без половины (Р22); без прав `du` молча считает
# неполную сумму, поэтому отказы считаются (`denied`, Р21).
#
# POSIX sh. Вывод — одна строка JSON, другой печати в stdout нет.

set -u

esc() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\000-\037'
}

has() { command -v "$1" >/dev/null 2>&1; }

user="$(id -un 2>/dev/null || echo "?")"
privileged=false
PRIV=""
if [ "$(id -u 2>/dev/null || echo 1000)" = "0" ]; then
    privileged=true
elif has sudo && sudo -n true 2>/dev/null; then
    privileged=true
    PRIV="sudo -n"
fi

# Потолок и низкий приоритет: сервер бывает одноядерным, а `du` по большому
# диску идёт минутами. У `ssh::run_script` своего предела нет — держит этот.
LONG=""
SHORT=""
if has timeout; then
    LONG="timeout 90"
    SHORT="timeout 30"
fi
GENTLE=""
has nice && GENTLE="nice -n 19"
has ionice && GENTLE="$GENTLE ionice -c3"

# ── Раздел и другие разделы ──────────────────────────────────────────────

root_fs="$(df -Pk / 2>/dev/null | awk 'NR == 2 { print $2 "|" $3 "|" $4 }')"

# Другие настоящие разделы: «точка|всего КБ|занято КБ;». Служебные файловые
# системы в памяти и слои контейнеров — не диск человека; то же устройство,
# что у корня (подтом btrfs, файл, подставленный в /etc), — тот же диск.
root_source="$(df -P / 2>/dev/null | awk 'NR == 2 { print $1 }')"
partitions="$(df -PTk 2>/dev/null | awk -v root="$root_source" '
    NR > 1 && $7 != "/" && $1 != root && $7 !~ /^\/etc\// && $2 !~ /^(tmpfs|devtmpfs|overlay|squashfs|proc|sysfs|cgroup2?|devpts|mqueue|debugfs|tracefs|securityfs|pstore|bpf|autofs|fusectl|configfs|hugetlbfs|ramfs|nsfs|efivarfs|binfmt_misc|fuse\..*|nfs.*|cifs|smb3|9p|vboxsf|shm)$/ && $7 !~ /^\/(proc|sys|dev|run)(\/|$)/ && $7 !~ /^\/snap\// && $7 !~ /[|;]/ {
        printf "%s|%s|%s;", $7, $3, $4
    }' | head -c 4000)"

# ── Папки корневого раздела, два уровня ──────────────────────────────────
# Вывод `du` и его ошибки идут одним потоком: строки данных — «число<TAB>путь»,
# остальное — отказы. Код выхода дописывается последней строкой: 124 — потолок.

folders=""
denied=0
timed_out=false
raw="$($LONG $PRIV $GENTLE du -x -k -d 2 / 2>&1; echo "rc=$?")"
rc="$(printf '%s\n' "$raw" | awk -F= '/^rc=/ { code = $2 } END { print code + 0 }')"
[ "$rc" = "124" ] && timed_out=true
denied="$(printf '%s\n' "$raw" | grep -c '^du: ')"
# Папки от 10 МБ, путь без «|» и «;» — разделителей ответа; не больше 60.
folders="$(printf '%s\n' "$raw" | awk -F'\t' '
    NF == 2 && $1 ~ /^[0-9]+$/ && $1 >= 10240 && $2 !~ /[|;]/ { print $1 "|" $2 }' |
    sort -t'|' -k1,1nr | head -n 60 | tr '\n' ';')"

# ── Журналы ──────────────────────────────────────────────────────────────
# Размеры каталогов, а не `journalctl --disk-usage`: без прав тот молча
# считает только своё (Р20). Каталог журналов контейнеров — вместе с их
# мелкими настройками: это и есть место, которое растёт.

logs="$($SHORT $PRIV du -k -s /var/log /var/log/journal /run/log/journal /var/lib/docker/containers 2>/dev/null |
    awk -F'\t' 'NF == 2 && $1 ~ /^[0-9]+$/ { printf "%s|%s;", $2, $1 }')"

# ── Docker ───────────────────────────────────────────────────────────────
# Тот же путь прав, что у `containers.sh`: root, группа docker или `sudo -n`.
# `docker system df` считает и чужие образы — здесь это то, что нужно: место
# на диске занимают все.

docker_state="none"
docker=""
if has docker; then
    DPRIV=""
    if ! $SHORT docker ps -q >/dev/null 2>&1 && [ -n "$PRIV" ]; then
        DPRIV="$PRIV"
    fi
    if out="$($SHORT $DPRIV docker system df --format '{{.Type}}|{{.Size}}|{{.Reclaimable}}' 2>/dev/null)"; then
        docker_state="ok"
        docker="$(printf '%s\n' "$out" | head -n 10 | tr '\n' ';')"
    else
        docker_state="unreadable"
    fi
fi

printf '{"user":"%s","privileged":%s,"root":"%s","partitions":"%s","folders":"%s","denied":%s,"timedOut":%s,"logs":"%s","dockerState":"%s","docker":"%s"}\n' \
    "$(esc "$user")" "$privileged" "$(esc "$root_fs")" "$(esc "$partitions")" "$(esc "$folders")" \
    "${denied:-0}" "$timed_out" "$(esc "$logs")" "$docker_state" "$(esc "$docker")"
