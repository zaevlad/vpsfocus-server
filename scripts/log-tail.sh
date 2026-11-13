#!/bin/sh
# Последние строки журнала контейнера или службы (трек AH, предложение 3,
# спринт 259). ТОЛЬКО ЧТЕНИЕ.
#
# По кнопке «Показать журнал» у контейнера («Проверить контейнеры») и у
# упавшей службы («Здоровье сервера»). Скрипт отдаётся на stdin (`sh -s`) и
# на диск сервера не попадает. Команды проверены `find / -xdev -newer` в
# контейнерах ubuntu:24.04 и debian:12 от root, через `sudo -n` и без прав
# (спринт 255): следов нет, кроме тех, что оставляет сам `sudo` (Р19).
#
# Параметры (дописывает приложение, `ИМЯ='значение'`):
#   KIND  — `container` или `service`;
#   NAME  — имя из списка, который экран только что прочитал; Rust пропускает
#           только буквы, цифры и `._-@:` (`logtail::name_ok`);
#   LINES — сколько последних строк, 100 (Р13).
#
# Строки уходят base64 одной строкой: в журнале бывают кавычки, переводы
# строк и не-UTF-8, а `esc()` перевод строки съел бы (П24). Цвета ANSI и
# длинные строки режет Rust. Предел ответа — последние 256 КБ до
# кодирования: журнал идёт от старых строк к новым, и при длинных строках
# (JSON, стеки ошибок) обрезка с начала потеряла бы самые свежие — те, где
# видна причина. Первая строка после обрезки бывает неполной.
#
# Без прав журнал не читается вовсе (Р20): `journalctl` у пользователя вне
# групп `adm`/`systemd-journal` молча отдаёт только его собственные строки —
# это было бы неправдой, а не «журнал пуст».
#
# POSIX sh. Вывод — одна строка JSON, другой печати в stdout нет.

set -u

has() { command -v "$1" >/dev/null 2>&1; }

esc() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\000-\037'
}

user="$(id -un 2>/dev/null || echo "?")"
LIMIT=""
has timeout && LIMIT="timeout 20"
MAX=262144

state="none"
driver=""
text=""

encode() {
    tail -c "$MAX" | base64 | tr -d '\n'
}

if ! has base64; then
    state="unreadable"
elif [ "$KIND" = "container" ]; then
    if has docker; then
        # Тот же путь прав, что у `containers.sh`: root, группа docker, `sudo -n`.
        PRIV=""
        if ! $LIMIT docker ps -q >/dev/null 2>&1; then
            if has sudo && sudo -n true 2>/dev/null; then
                PRIV="sudo -n"
            fi
        fi
        if driver="$($LIMIT $PRIV docker inspect --format '{{.HostConfig.LogConfig.Type}}' "$NAME" 2>/dev/null)"; then
            if [ "$driver" = "none" ]; then
                state="noLogs"
            # Программа пишет и в stdout, и в stderr — нужны оба потока.
            elif out="$($LIMIT $PRIV docker logs --tail "$LINES" --timestamps "$NAME" 2>&1)"; then
                state="ok"
                text="$(printf '%s\n' "$out" | encode)"
            else
                case "$out" in
                    *"does not support reading"*) state="noLogs" ;;
                    *) state="unreadable" ;;
                esac
            fi
        elif $LIMIT $PRIV docker ps -q >/dev/null 2>&1; then
            # Docker отвечает, а контейнера нет: убрали после проверки списка.
            state="missing"
        else
            state="unreadable"
        fi
    fi
elif [ "$KIND" = "service" ]; then
    if has journalctl; then
        PRIV=""
        allowed=false
        if [ "$(id -u 2>/dev/null || echo 1000)" = "0" ]; then
            allowed=true
        elif id -Gn 2>/dev/null | tr ' ' '\n' | grep -qx -e adm -e systemd-journal; then
            allowed=true
        elif has sudo && sudo -n true 2>/dev/null; then
            allowed=true
            PRIV="sudo -n"
        fi
        if [ "$allowed" = "true" ]; then
            if out="$($LIMIT $PRIV journalctl -u "$NAME" -n "$LINES" --no-pager -q -o short-iso 2>/dev/null)"; then
                state="ok"
                text="$(printf '%s\n' "$out" | encode)"
            else
                state="unreadable"
            fi
        else
            state="noRights"
        fi
    fi
fi

printf '{"state":"%s","user":"%s","driver":"%s","text":"%s"}\n' \
    "$state" "$(esc "$user")" "$(esc "$driver")" "$text"
