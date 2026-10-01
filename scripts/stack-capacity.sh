#!/bin/sh
# Сколько места занял мониторинг и сколько свободно на диске — по кнопке на
# экране «Запас ресурсов». ТОЛЬКО ЧТЕНИЕ. Модуль: stack.rs (capacity).
#
# `du -sm` вместо `docker system df`: последний считает заодно чужие образы
# клиента. Нас спрашивают про наш стек. Тома Docker лежат в
# /var/lib/docker/volumes, куда заглядывает только root, — поэтому по SSH, а
# не агентом. Отсутствующий том — пустая строка, приложение читает её нулём.
#
# Параметры:
#   DIR   — каталог стека, /opt/vpsfocus;
#   STACK — имя проекта compose, vpsfocus: из него Docker собирает имена томов.

SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
VOLUMES=/var/lib/docker/volumes
size() { $SUDO du -sm "$1" 2>/dev/null | cut -f1 || true; }
printf '::analytics:%s\n' "$(size "$VOLUMES/${STACK}_postgres-data")"
printf '::agent:%s\n' "$(size "$VOLUMES/${STACK}_agent-data")"
printf '::edge:%s\n' "$(size "$VOLUMES/${STACK}_caddy-data")"
printf '::files:%s\n' "$(size "$DIR")"
df -Pm / | awk 'NR==2 { printf "::disk:%s:%s\n", $2, $4 }'
