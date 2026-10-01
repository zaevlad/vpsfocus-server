#!/bin/sh
# Кнопка «Убрать следы» прерванной установки. Выполняется под замком
# lock-opt.sh. МЕНЯЕТ: доделывает откат rollback.sh, удаляет каталог прогона и
# — только если установка не успела ничего записать — каталог стека.
# Модуль: stack.rs (cleanup_script).
#
# Решений здесь нет: зовём откат и смотрим, остался ли каталог. rollback.sh
# сам отказывается трогать стек, который стоял на сервере до нас.
#
# Прерванное обновление этой кнопке не по зубам: откат у стоящего стека
# откажется, а снимок обновления он не возвращает — уборка стёрла бы только
# улику. Такой прогон остаётся как есть.
#
# Установку, убитую в первые секунды, — до того, как install.sh записал
# .install-state, — rollback.sh принимает за стоявший стек и отказывается.
# Но до той записи скрипт ничего на сервере не меняет, а stack-version.json
# оставляет только законченная установка: нет обоих файлов — в каталоге лишь
# наша заливка, и убрать его можно.
#
# Параметры:
#   DIR — каталог стека, /opt/vpsfocus;
#   RUN — каталог прогона, /opt/vpsfocus.run.

SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
if $SUDO grep -q '^kind=update$' "$RUN/meta" 2>/dev/null; then printf '::left\n'; exit 0; fi
if $SUDO test -f "$DIR/rollback.sh"; then
    $SUDO sh "$DIR/rollback.sh" >/dev/null 2>&1 || true
fi
if $SUDO test -d "$DIR" && ! $SUDO test -f "$DIR/.install-state" && ! $SUDO test -f "$DIR/stack-version.json"; then
    $SUDO rm -rf "$DIR"
fi
$SUDO rm -rf "$RUN"
if $SUDO test -d "$DIR"; then printf '::left\n'; else printf '::cleaned\n'; fi
