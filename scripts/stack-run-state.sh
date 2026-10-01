#!/bin/sh
# Есть ли на сервере незаконченная установка — без чтения её журнала.
# ТОЛЬКО ЧТЕНИЕ. Модуль: stack.rs (run_state).
#
# Нужен экрану установки при открытии: показать незаконченный прогон он
# обязан сразу, а дочитывать журнал — уже отдельной, долгой командой.
#
# Параметры:
#   RUN — каталог прогона, /opt/vpsfocus.run.

SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
$SUDO test -d "$RUN" || { printf '::none\n'; exit 0; }
printf '::meta\n'
$SUDO cat "$RUN/meta" 2>/dev/null || true
printf '::done\n'
$SUDO cat "$RUN/done" 2>/dev/null || true
printf '::live\n'
PID="$($SUDO cat "$RUN/pid" 2>/dev/null || true)"
if [ -n "$PID" ] && $SUDO kill -0 "$PID" 2>/dev/null; then echo 1; else echo 0; fi
