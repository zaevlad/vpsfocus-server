#!/bin/sh
# Общий календарь команды, /opt/vpsfocus/calendar.json. ТОЛЬКО ЧТЕНИЕ.
# Модуль: calendar_share.rs (read_with). Запись — write-file.sh.
#
# `::stack` — мониторинг на сервере стоит (без него делиться календарём не
# через что). Хеш считает сервер: по нему запись узнаёт, не изменил ли файл
# кто-то из команды.
#
# Параметры:
#   DIR  — каталог стека, /opt/vpsfocus;
#   FILE — имя файла календаря, calendar.json.

SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
if $SUDO test -f "$DIR/stack-version.json"; then echo '::stack'; fi
echo '::hash'
$SUDO sha256sum "$DIR/$FILE" 2>/dev/null | cut -d' ' -f1 || true
echo '::content'
$SUDO cat "$DIR/$FILE" 2>/dev/null || true
