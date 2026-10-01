#!/bin/sh
# «Данные сайта»: адрес базы, которая работает в Docker-контейнере, — чтобы
# открыть к ней туннель SSH. ТОЛЬКО ЧТЕНИЕ. Модуль: query/docker.rs
# (remote_host).
#
# `--type container`: без него `docker inspect` отвечает про любой объект с
# таким именем, и образ `mysql` при удалённом контейнере `mysql` дал бы
# вместо «контейнера нет» ошибку шаблона.
#
# `timeout` снаружи `sudo`: право на `sudo timeout` в sudoers дают реже, чем на
# `sudo docker`. Годность проверяется пробой `timeout 1 true`: BusyBox до 1.30
# знает только запись `timeout -t`.
#
# Параметры:
#   NAME    — имя контейнера; приложение пропускает только имена Docker
#             (буквы, цифры, `_.-`);
#   SECONDS_LIMIT — потолок времени для `docker`.
#
# Вывод: `true 172.18.0.3 ` или `false `; нет Docker — метка в stderr.

SUDO=""; [ "$(id -u)" = 0 ] || SUDO="sudo -n"
command -v docker >/dev/null 2>&1 || { echo 'vpsfocus:docker=missing' >&2; exit 0; }
T=""; timeout 1 true >/dev/null 2>&1 && T="timeout $SECONDS_LIMIT"
$T $SUDO docker inspect --type container --format '{{.State.Running}} {{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' "$NAME"
