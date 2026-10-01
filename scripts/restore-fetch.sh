#!/bin/sh
# Сервер сам скачивает копию из хранилища клиента перед восстановлением.
# Модуль: lifecycle.rs (fetch_copy).
# МЕНЯЕТ: кладёт копию в $DIR/$RESTORE/copy.tar.gz (права 600); дальше её
# разбирает и удаляет restore.sh.
#
# Ссылку подписывает агент этого же сервера по ключам хранилища клиента.
# Она не попадает ни в один список процессов: скрипт приезжает на stdin
# (в `ps` виден только `sh -s`), а curl и wget получают её на своём stdin,
# а не аргументом.
#
# Параметры:
#   DIR     — каталог стека, /opt/vpsfocus;
#   RESTORE — имя временного каталога копии, .restore;
#   URL     — подписанная ссылка на объект в хранилище клиента.

set -eu
SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
TARGET=$DIR/$RESTORE
$SUDO sh -c "umask 077; mkdir -p $TARGET"

if command -v curl >/dev/null 2>&1; then
    printf 'url = "%s"\n' "$URL" | $SUDO sh -c "umask 077; curl -fsS -K - -o $TARGET/copy.tar.gz"
elif command -v wget >/dev/null 2>&1; then
    printf '%s\n' "$URL" | $SUDO sh -c "umask 077; wget -q -i - -O $TARGET/copy.tar.gz"
else
    echo '::nodownloader'
    exit 0
fi
echo '::fetched'
