#!/bin/sh
# Кто может войти на сервер (трек AH, предложение 5, спринт 261). ТОЛЬКО ЧТЕНИЕ.
#
# Зовётся проверкой «Безопасности» вслед за `server-check.sh`, той же сессией
# SSH. Скрипт отдаётся на stdin (`sh -s`) и на диск сервера не попадает.
# Команды проверены `find / -xdev -newer` в контейнерах ubuntu:24.04 и
# debian:12 с systemd от root, через `sudo -n` и без прав (спринты 255, 261):
# следов нет, кроме тех, что оставляет сам `sudo` (Р19). Через `sudo` идут
# три команды: `sshd -T`, чтение файлов ключей одним `awk` и журнал входов.
#
# Факты, а не оценки: кто «может войти», решает `serveraccess.rs`.
#
# * Пользователи — из `/etc/passwd`, все: вход для туннелей бывает с
#   оболочкой `nologin`, а ключ у него есть (П16).
# * Ключи — по `AuthorizedKeysFile` из итоговых настроек sshd (по умолчанию
#   `.ssh/authorized_keys .ssh/authorized_keys2`). **Сам ключ с сервера не
#   уходит:** `ssh-keygen -lf -` печатает только длину, отпечаток, подпись и
#   вид. Без прав читается только свой файл — чужие «не знаем» (П22).
# * Входы — строки `Accepted …` журнала sshd за 30 дней (`-o short-unix`,
#   Р24), запасные — `/var/log/auth.log`, `/var/log/secure`; `last` и wtmp
#   нет (П15). Без прав журнал молча отдаёт только своё (Р20) — не читаем.
#   Группируются здесь же, в `awk`: наружу уходит не больше 200 групп.
#
# POSIX sh. Вывод — одна строка JSON, другой печати в stdout нет.

set -u

has() { command -v "$1" >/dev/null 2>&1; }

esc() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\000-\037'
}

# Поле без разделителей ответа: «|» и «;» внутри подписи ключа заменяются.
field() { printf '%s' "$1" | tr '|;' '//' | tr -d '\000-\037'; }

user="$(id -un 2>/dev/null || echo "?")"
uid="$(id -u 2>/dev/null || echo 1000)"
privileged=false
PRIV=""
if [ "$uid" = "0" ]; then
    privileged=true
elif has sudo && sudo -n true 2>/dev/null; then
    privileged=true
    PRIV="sudo -n"
fi
LIMIT=""
has timeout && LIMIT="timeout 20"

# ── Итоговые настройки sshd ──────────────────────────────────────────────

sshd_bin=""
if has sshd; then
    sshd_bin="sshd"
elif [ -x /usr/sbin/sshd ]; then
    sshd_bin="/usr/sbin/sshd"
fi
conf=""
sshd_known=false
if [ -n "$sshd_bin" ] && [ "$privileged" = "true" ] && conf="$($LIMIT $PRIV "$sshd_bin" -T 2>/dev/null)" && [ -n "$conf" ]; then
    sshd_known=true
fi
setting() {
    printf '%s\n' "$conf" | awk -v name="$1" 'tolower($1) == name { $1 = ""; sub(/^ /, ""); print; exit }'
}
keys_file="$(setting authorizedkeysfile)"
[ -n "$keys_file" ] || keys_file=".ssh/authorized_keys .ssh/authorized_keys2"
keys_command="$(setting authorizedkeyscommand)"
allow_users="$(setting allowusers)"
deny_users="$(setting denyusers)"
allow_groups="$(setting allowgroups)"
deny_groups="$(setting denygroups)"
root_login="$(setting permitrootlogin)"
password_login="$(setting passwordauthentication)"

# ── Пользователи и их файлы ключей ───────────────────────────────────────
# «имя|uid|оболочка;» — все из /etc/passwd, не больше 300.

users="$(awk -F: 'NF >= 7 && $1 !~ /[|;]/ { printf "%s|%s|%s;", $1, $3, $7 }' /etc/passwd 2>/dev/null | head -c 20000)"

# Кандидаты в файлы ключей: «пользователь<TAB>путь» по каждому шаблону.
candidates="$(awk -F: -v patterns="$keys_file" '
    NF >= 7 && $6 != "" && $6 != "/" && $6 !~ /^\/nonexistent/ {
        n = split(patterns, list, " ")
        for (i = 1; i <= n; i++) {
            path = list[i]
            gsub(/%%/, "\001", path)
            gsub(/%h/, $6, path)
            gsub(/%u/, $1, path)
            gsub(/\001/, "%", path)
            if (substr(path, 1, 1) != "/") path = $6 "/" path
            print $1 "\t" path
        }
    }' /etc/passwd 2>/dev/null | head -n 1000)"

keys=""
if [ -n "$candidates" ]; then
    # Одним `grep` через `sudo`: «путь:строка» для каждой строки-ключа.
    # `-s` молча пропускает отсутствующие файлы (`awk` в Ubuntu — mawk — на
    # первом таком остановился бы). Путь с двоеточием не берётся: им разделён
    # ответ `grep`. Без прав читается только доступное этому пользователю —
    # чужие ключи тогда «не знаем».
    lines="$(printf '%s\n' "$candidates" | cut -f2 | grep -v ':' | sort -u |
        $LIMIT $PRIV xargs grep -Hsv -e '^[[:space:]]*#' -e '^[[:space:]]*$' 2>/dev/null | head -n 300)"
    keys="$(printf '%s\n' "$lines" | while IFS= read -r entry; do
        [ -n "$entry" ] || continue
        path="${entry%%:*}"
        line="${entry#*:}"
        owner="$(printf '%s\n' "$candidates" | awk -F'\t' -v p="$path" '$2 == p { print $1; exit }')"
        # Опции строки (`command="…"`, `environment="…"`) с сервера не уходят:
        # в них бывают секреты, а у ключа без подписи `ssh-keygen` напечатал
        # бы их на месте подписи. Ключ — с поля вида ключа, за которым base64.
        bare="$(printf '%s\n' "$line" | awk '{ for (i = 1; i < NF; i++) if ($i ~ /^(ssh-|ecdsa-sha2-|sk-)/ && $(i + 1) ~ /^AAAA/) { out = $i; for (j = i + 1; j <= NF; j++) out = out " " $j; print out; exit } }')"
        [ -n "$bare" ] || continue
        restricted=0
        case "$line" in
            ssh-* | ecdsa-sha2-* | sk-*) ;;
            *) restricted=1 ;;
        esac
        printed="$(printf '%s\n' "$bare" | ssh-keygen -lf - 2>/dev/null | head -n 1)"
        [ -n "$printed" ] || continue
        printf '%s|%s|%s|%s;' "$(field "$owner")" "$(field "$path")" "$restricted" "$(field "$printed")"
    done)"
fi

# ── Удачные входы за 30 дней ─────────────────────────────────────────────
# Группа: «пользователь|способ|адрес|вид ключа|отпечаток|число|последний;».
# Время — секундами эпохи из журнала или сырой строкой из auth.log.
# Способ бывает со «/»: `keyboard-interactive/pam` — пароль через PAM; хвост
# после «/» отрезается, иначе такие входы молча пропали бы из списка.
# Групп не больше 200, свежие первыми; обрезали — признак `loginsMore`.

group_awk='
    match($0, /Accepted [a-z\/-]+ for [^ ]+ from [^ ]+ port [0-9]+/) {
        n = split(substr($0, RSTART, RLENGTH), part, " ")
        method = part[2]; who = part[4]; addr = part[6]
        sub(/\/.*/, "", method)
        type = ""; fp = ""
        if (match($0, /ssh2: [A-Z0-9-]+ SHA256:[A-Za-z0-9+\/=]+/)) {
            split(substr($0, RSTART, RLENGTH), k, " ")
            type = k[2]; fp = k[3]
        }
        key = who "|" method "|" addr "|" type "|" fp
        count[key]++
        last[key] = stamp($0)
    }
    END { for (key in count) printf "%s|%d|%s;\n", key, count[key], last[key] }'

# Группы — свежие первыми (у журнала время числом), не больше 200. Функция
# идёт в подоболочке `$(…)`, поэтому «обрезали» она отдаёт меткой в конце
# вывода, а не переменной (файл на диск сервера не пишем).
logins_more=false
cap_groups() {
    groups="$(sort -t'|' -k7,7nr)"
    printf '%s\n' "$groups" | head -n 200 | tr -d '\n'
    if [ "$(printf '%s\n' "$groups" | grep -c .)" -gt 200 ]; then
        printf '@@more'
    fi
}
take_more() {
    case "$logins" in
        *@@more)
            logins_more=true
            logins="${logins%@@more}"
            ;;
    esac
}

logins=""
logins_source="none"
logins_from=""
persistent=false
[ -d /var/log/journal ] && persistent=true
if [ "$privileged" = "true" ]; then
    if has journalctl; then
        raw="$($LIMIT $PRIV journalctl -u ssh -u sshd --since '-30d' --no-pager -q -o short-unix 2>/dev/null)"
        if [ -n "$raw" ]; then
            logins_source="journal"
            logins_from="$(printf '%s\n' "$raw" | awk 'NR == 1 { split($1, s, "."); print s[1] }')"
            logins="$(printf '%s\n' "$raw" | awk 'function stamp(line) { split($1, s, "."); return s[1] }'"$group_awk" | cap_groups)"
            take_more
        fi
    fi
    if [ "$logins_source" = "none" ]; then
        for file in /var/log/auth.log /var/log/secure; do
            [ -e "$file" ] || continue
            raw="$($LIMIT $PRIV grep -a 'sshd' "$file" 2>/dev/null | tail -n 20000)"
            [ -n "$raw" ] || continue
            logins_source="authlog"
            # Время в auth.log — «Nov 13 08:39:50» или RFC 3339 у rsyslog;
            # разбирает Rust.
            logins_from="$(printf '%s\n' "$raw" | awk 'NR == 1 { if ($1 ~ /^[0-9][0-9][0-9][0-9]-/) print $1; else print $1 " " $2 " " $3 }')"
            logins="$(printf '%s\n' "$raw" | awk 'function stamp(line) { if ($1 ~ /^[0-9]/) return $1; return $1 " " $2 " " $3 }'"$group_awk" | cap_groups)"
            take_more
            break
        done
    fi
fi

# Адрес этой сессии — по нему экран узнаёт свои входы (П14).
session_addr="$(printf '%s' "${SSH_CLIENT:-}" | awk '{ print $1 }')"

printf '{"user":"%s","privileged":%s,"sshdKnown":%s,"keysFile":"%s","keysCommand":"%s","allowUsers":"%s","denyUsers":"%s","allowGroups":"%s","denyGroups":"%s","rootLogin":"%s","passwordLogin":"%s","users":"%s","keys":"%s","logins":"%s","loginsSource":"%s","loginsFrom":"%s","loginsMore":%s,"persistent":%s,"sessionAddr":"%s","now":%s}\n' \
    "$(esc "$user")" "$privileged" "$sshd_known" "$(esc "$keys_file")" "$(esc "$keys_command")" \
    "$(esc "$allow_users")" "$(esc "$deny_users")" "$(esc "$allow_groups")" "$(esc "$deny_groups")" \
    "$(esc "$root_login")" "$(esc "$password_login")" "$(esc "$users")" "$(esc "$keys")" \
    "$(esc "$logins")" "$logins_source" "$(esc "$logins_from")" "$logins_more" "$persistent" \
    "$(esc "$session_addr")" "$(date +%s 2>/dev/null || echo 0)"
