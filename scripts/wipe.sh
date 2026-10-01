#!/bin/sh
# Снос мониторинга с сервера: «Удалить сервер» с галочкой «снести и
# мониторинг». Выполняется под замком lock-opt.sh. Модуль: lifecycle.rs
# (wipe_script).
# МЕНЯЕТ: снимает выданный нами доступ к логам (ACL), зовёт rollback.sh
# --force (контейнеры, тома, правило файрвола), удаляет каталог стека.
#
# Доступ к логам снимается до сноса каталога: список выданного лежит в нём
# самом. Мы поставили — нам и убирать.
#
# Docker снос не удаляет никогда (Б1 ревизии 2026-09-18): за год на
# поставленном нами Docker клиент заводит свои контейнеры. Нынешний
# rollback.sh с --force Docker не трогает и сам, но на серверах лежит тот,
# что приехал с их бандлом, — поэтому отметка DOCKER_INSTALLED гасится здесь:
# строка дописывается в конец .install-state, который rollback.sh любой
# версии читает через `.`, и последнее присваивание побеждает. Не
# дописалось — откат не зовём вовсе и отвечаем отказом: снести стек можно и
# позже, а Docker клиента обратно не вернуть.
#
# Параметры:
#   DIR — каталог стека, /opt/vpsfocus.

SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
if $SUDO test -f "$DIR/log-access.list" && command -v setfacl >/dev/null 2>&1; then
    $SUDO cat "$DIR/log-access.list" | while read -r granted; do
        [ -n "$granted" ] || continue
        $SUDO setfacl -x g:adm "$granted" 2>/dev/null || true
        $SUDO setfacl -d -x g:adm "$granted" 2>/dev/null || true
    done
fi
if $SUDO test -f "$DIR/rollback.sh"; then
    if $SUDO test -f "$DIR/.install-state" &&
        ! printf '\nDOCKER_INSTALLED=0\n' | $SUDO tee -a "$DIR/.install-state" >/dev/null; then
        echo 'install-state not writable: rollback.sh not run' >&2
        exit 1
    fi
    $SUDO sh "$DIR/rollback.sh" --force >/dev/null 2>&1 || true
fi
$SUDO rm -rf "$DIR"
if $SUDO test -d "$DIR"; then echo '::left'; else echo '::wiped'; fi
