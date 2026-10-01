#!/bin/sh
# Доступ мониторинга к логам веб-сервера: кнопки «Разрешить чтение» и
# «Снять доступ» на экране логов. Модуль: agent.rs (log_access_registry,
# change_log_access).
#
# Логи почти везде лежат с правами 640 root:adm, и контейнеру агента группа
# adm выдана всегда. Где этого не хватило (на RHEL каталог закрыт целиком),
# человек разрешает чтение кнопкой: группе adm добавляется ACL на каталог и
# файлы логов. Права файлов и их владельца не трогаем.
#
# Это одно из двух изменений за пределами нашего каталога (второе — правило
# файрвола при установке). Оно обратимо: список каталогов, которым мы дали
# доступ, лежит в $REG, и по нему снимает доступ и эта кнопка, и снос стека.
#
# Параметры:
#   ACTION — list (ТОЛЬКО ЧТЕНИЕ: список выданного), grant или revoke
#            (МЕНЯЕТ: ACL на каталоге и файлах логов, строка в $REG);
#   DIR    — каталог лога (для list не нужен). Приложение пропускает только
#            абсолютный путь без кавычек, `$`, `;`, `&`, `|` и `..`, и в
#            имени файла обязано быть «log»;
#   REG    — список выданного, /opt/vpsfocus/log-access.list.

set -eu
SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"

if [ "$ACTION" = list ]; then
    # Файла может не быть — значит, мы ничего не выдавали.
    $SUDO cat "$REG" 2>/dev/null || true
    exit 0
fi

[ -d "$DIR" ] || { echo ::missing; exit 0; }
# Пакеты на чужой сервер мы не ставим: нет setfacl — говорим об этом
# человеку, а не решаем за него.
command -v setfacl >/dev/null 2>&1 || { echo ::no-acl; exit 0; }

if [ "$ACTION" = grant ]; then
    $SUDO setfacl -m g:adm:rx "$DIR"
    # Умолчание для файлов, которые появятся после поворота лога.
    $SUDO setfacl -d -m g:adm:r "$DIR" 2>/dev/null || true
    for file in "$DIR"/*log*; do [ -f "$file" ] && $SUDO setfacl -m g:adm:r "$file" 2>/dev/null || true; done
    $SUDO touch "$REG"
    $SUDO grep -qxF "$DIR" "$REG" 2>/dev/null || echo "$DIR" | $SUDO tee -a "$REG" >/dev/null
    echo ::granted
else
    # Снимаем только то, что сами и выдали: право читать логи мог дать и
    # администратор сервера, задолго до нас и по своим причинам.
    $SUDO grep -qxF "$DIR" "$REG" 2>/dev/null || { echo ::not-ours; exit 0; }
    $SUDO setfacl -x g:adm "$DIR" 2>/dev/null || true
    $SUDO setfacl -d -x g:adm "$DIR" 2>/dev/null || true
    for file in "$DIR"/*log*; do [ -f "$file" ] && $SUDO setfacl -x g:adm "$file" 2>/dev/null || true; done
    if [ -f "$REG" ]; then $SUDO sh -c "grep -vxF '$DIR' '$REG' > '$REG.new' || true; mv '$REG.new' '$REG'"; fi
    echo ::revoked
fi
