#!/bin/sh
# Вход поиска сайтов (discovery.sh): чем читать диск и чем спрашивать Docker.
# ТОЛЬКО ЧТЕНИЕ. Тест ставит вместо этого файла свой вход с поддельным
# `docker`, а текст поиска гоняет тот же самый.
#
# С Docker говорим как получится: вход в группе docker обходится без sudo. Не
# пустили ни так, ни так — `::denied:docker`, а не молчание. Каждый вызов —
# с потолком времени: зависший демон Docker на чужом сервере оставил бы
# кнопку «Найти сайты» крутиться вечно.

SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"

LIMIT=""
command -v timeout >/dev/null 2>&1 && LIMIT="timeout 15"
DOCKER=""
if command -v docker >/dev/null 2>&1; then
    if $LIMIT docker ps -q >/dev/null 2>&1; then
        DOCKER="$LIMIT docker"
    elif [ -n "$SUDO" ] && $LIMIT $SUDO docker ps -q >/dev/null 2>&1; then
        DOCKER="$LIMIT $SUDO docker"
    elif [ -n "$SUDO" ]; then
        printf '::denied:docker\n'
    fi
fi
