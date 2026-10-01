#!/bin/sh
# Перезапуск одного контейнера мониторинга по кнопке в «Диагностике».
# МЕНЯЕТ: перезапускает контейнер нашего стека. Модуль: stack.rs
# (restart_service).
#
# По SSH, а не агентом: сокет Docker в контейнере агента равен root на
# сервере, и его агенту не дают ни ради какой функции.
#
# Параметры:
#   DIR     — каталог стека, /opt/vpsfocus;
#   SERVICE — postgres, umami, caddy или agent. Приложение принимает только
#             эти четыре имени (белый список RESTARTABLE).

set -e
SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
cd "$DIR"
$SUDO docker compose restart "$SERVICE"
