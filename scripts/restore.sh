#!/bin/sh
# Восстановление аналитики из копии. Выполняется под замком lock-opt.sh.
# Модуль: lifecycle.rs (restore_script).
# МЕНЯЕТ: останавливает Umami, сбрасывает схему базы аналитики и вливает
# дамп из копии, поднимает Umami. Копия к этому моменту лежит в
# $DIR/$RESTORE/copy.tar.gz (restore-upload.sh или restore-fetch.sh); каталог
# удаляется ловушкой при любом исходе.
#
# Порядок объясняется тем, что копия бывает старше стека: гасим Umami, сбрасываем
# схему, вливаем дамп, поднимаем Umami — и она при старте догоняет схему
# своими миграциями.
#
# Параметры:
#   DIR     — каталог стека, /opt/vpsfocus;
#   RESTORE — имя временного каталога копии внутри него, .restore;
#   CONFIGS — 1: отдать приложению состав проектов и ключевые адреса из копии
#             (оно запишет их само — писатель у этих файлов один); пусто — нет.
#
# Метки в stdout: `::step:…` — шаги для экрана; `::nocopy`, `::badarchive`,
# `::nodump` — копия не годится, на сервере ничего не изменено;
# `::schemafailed`, `::loadfailed`, `::startfailed` — отказ после остановки
# Umami; `::restored` — удача.

set -eu
SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
COPY=$DIR/$RESTORE/copy.tar.gz
WORK=$DIR/$RESTORE/unpacked

# Уборка ловушкой, а не строкой в конце удачного пути: в каталоге вся
# аналитика клиента, и оставлять её на чужом диске нельзя ни в каком исходе.
cleanup() { $SUDO rm -rf "$DIR/$RESTORE"; }
trap cleanup EXIT

$SUDO test -s "$COPY" || { echo '::nocopy'; exit 0; }

echo '::step:unpack'
$SUDO rm -rf "$WORK"
$SUDO sh -c "umask 077; mkdir -p $WORK"
$SUDO tar -xzf "$COPY" -C "$WORK" || { echo '::badarchive'; exit 0; }

# Дамп — единственное, без чего восстанавливать нечего.
$SUDO test -s "$WORK/umami.sql" || { echo '::nodump'; exit 0; }
# Размер дампа считается внутри sudo: каталог распаковки создан с umask 077
# под root, и у входа без root перенаправление `<` его бы не прочитало.
DUMP=$($SUDO sh -c "wc -c < $WORK/umami.sql" | tr -d ' ')

if $SUDO test -f "$WORK/stack-version.json"; then
    printf '::stack:%s\n' "$($SUDO sed -n 's/.*"stackVersion"[^"]*"\([^"]*\)".*/\1/p' "$WORK/stack-version.json" | head -n1)"
fi

cd "$DIR"

# Umami гасится до того, как трогать схему: живая она держит соединения и
# пишет в те самые таблицы, которые мы собираемся заменить.
echo '::step:stop'
$SUDO docker compose stop umami >/dev/null 2>&1 || true

echo '::step:load'
# Схема сбрасывается целиком, а не «дамп поверх»: копия могла быть снята
# стеком другой версии, и остатки чужих таблиц сломали бы миграции Umami.
$SUDO docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U umami -d umami \
    -c 'drop schema public cascade; create schema public;' >/dev/null 2>&1 \
    || { echo '::schemafailed'; exit 0; }

$SUDO sh -c "cd $DIR && docker compose exec -T postgres psql -q -v ON_ERROR_STOP=1 -U umami -d umami < $WORK/umami.sql" >/dev/null \
    || { echo '::loadfailed'; exit 0; }

echo '::step:start'
$SUDO docker compose up -d umami >/dev/null 2>&1 || { echo '::startfailed'; exit 0; }

# Ждём, пока Umami догонит схему своими миграциями.
waited=0
until $SUDO docker compose exec -T umami node -e \
    "fetch('http://127.0.0.1:3000/api/heartbeat').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))" \
    >/dev/null 2>&1; do
    waited=$((waited + 3))
    if [ "$waited" -ge 180 ]; then echo '::notready'; break; fi
    sleep 3
done

printf '::dump:%s\n' "$DUMP"

# Конфиги отдаются приложению текстом, а не остаются на сервере: писатель у
# них один, и восстановленные на месте они прожили бы до первой записи из
# приложения.
if [ "$CONFIGS" = 1 ]; then
    for name in projects probes; do
        file="$WORK/agent-config/$name.json"
        if $SUDO test -f "$file"; then
            echo "::configs:$name:begin"
            $SUDO cat "$file"
            echo ""
            echo "::configs:$name:end"
        fi
    done
fi

echo '::step:cleanup'
# Сама уборка — в ловушке на выходе: она обязана случиться и тогда, когда
# восстановление не удалось. Здесь только шаг для экрана.

echo '::restored'
