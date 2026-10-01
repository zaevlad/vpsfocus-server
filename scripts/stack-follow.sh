#!/bin/sh
# Чтение журнала установки, которая идёт на сервере сама. ТОЛЬКО ЧТЕНИЕ.
# Модуль: stack.rs (follow_script).
#
# Обёртка: отдаёт тело (stack-follow-body.sh) оболочке под root — журнал
# прогона закрыт для всех, кроме root.

SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
exec $SUDO sh -s <<'VPSFOCUS_FOLLOW_EOF_5c8e02'
@BODY@
VPSFOCUS_FOLLOW_EOF_5c8e02
