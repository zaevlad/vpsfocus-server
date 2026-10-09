<#
.SYNOPSIS
Собирает и публикует образ агента.

.DESCRIPTION
Тег указан в двух местах: здесь и в шаблоне docker-compose бандла
(installer-bundle/versions/*/docker-compose.yml.tpl). Меняя версию, поменять
надо оба — иначе установка уйдёт за образом, которого нет.

Публикация требует входа в реестр:

    $env:CR_PAT = "<токен GitHub с правом write:packages>"
    $env:CR_PAT | docker login ghcr.io -u <логин> --password-stdin

.PARAMETER Load
Собрать только под архитектуру этой машины и оставить образ в локальном
демоне: так гоняются тесты против стенда, без публикации.

.PARAMETER Push
Собрать под amd64 и arm64 и отправить в реестр. ARM-серверы обычны у Hetzner
и Oracle Cloud, и односоставный образ у половины клиентов просто не
запустится.
#>
[CmdletBinding()]
param(
    [string] $Image = 'ghcr.io/zaevlad/vpsfocus-agent',
    [string] $Tag = '0.24.0',
    [switch] $Load,
    [switch] $Push
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# docker пишет прогресс сборки в stderr, а PowerShell 5.1 при
# ErrorActionPreference = Stop считает любую строку в stderr нативной команды
# ошибкой и роняет скрипт на успешной сборке. Поэтому вокруг вызовов docker
# режим временно смягчается, а результат проверяется по коду возврата.
function Invoke-Docker {
    param([Parameter(ValueFromRemainingArguments = $true)] [string[]] $Arguments)

    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        & docker @Arguments
    } finally {
        $ErrorActionPreference = $previous
    }

    if ($LASTEXITCODE -ne 0) {
        throw "docker $($Arguments -join ' ') завершился с кодом $LASTEXITCODE"
    }
}

if (-not $Load -and -not $Push) {
    Write-Host 'Укажите -Load (локальная сборка) или -Push (публикация в реестр).'
    exit 1
}

$context = Split-Path -Parent $MyInvocation.MyCommand.Path
$reference = "${Image}:${Tag}"

if ($Load) {
    Write-Host "Сборка $reference под архитектуру этой машины..."
    Invoke-Docker buildx build --load --tag $reference $context

    $size = & docker image inspect $reference --format '{{.Size}}'
    Write-Host ("Готово: $reference, {0:N1} МБ." -f ([double]$size / 1MB))
    exit 0
}

Write-Host "Сборка и публикация $reference под linux/amd64 и linux/arm64..."

# Дайджест опубликованного индекса (Б5 ревизии 2026-09-18). Бандл тянет
# образ по нему, а не по тегу: тег в реестре перезаписываем. Узнать дайджест
# можно только у реестра, после публикации, — buildx пишет его в файл
# метаданных, откуда он уходит в published.txt рядом с этим скриптом. Тест
# image_tags_match_the_build_scripts с этого момента требует ту же ссылку в
# шаблоне compose текущего бандла.
$metadata = Join-Path ([IO.Path]::GetTempPath()) "vpsfocus-agent-$([guid]::NewGuid()).json"
try {
    # Кавычки вокруг списка платформ обязательны. Без них PowerShell видит
    # `linux/amd64,linux/arm64` как массив из двух строк, а
    # ValueFromRemainingArguments его разворачивает — docker получает
    # `--platform linux/amd64 linux/arm64`, то есть второй платформой
    # оказывается аргумент, и сборка падает с «invalid component».
    Invoke-Docker buildx build --platform 'linux/amd64,linux/arm64' --push `
        --metadata-file $metadata --tag $reference $context
    $digest = (Get-Content -Raw -Path $metadata | ConvertFrom-Json).'containerimage.digest'
} finally {
    Remove-Item -Force -ErrorAction SilentlyContinue $metadata
}

if ($digest -notmatch '^sha256:[0-9a-f]{64}$') {
    throw "buildx не сообщил дайджест опубликованного образа: '$digest'"
}

$pinned = "${reference}@${digest}"
# ASCII без BOM: файл читает тест на Rust, и лишние байты в начале сделали
# бы ссылку непохожей на ту, что в шаблоне.
[IO.File]::WriteAllText((Join-Path $context 'published.txt'), "$pinned`n", [Text.Encoding]::ASCII)

Write-Host "Опубликовано: $pinned"
Write-Host 'Впишите эту ссылку в image: агента в docker-compose.yml.tpl новой версии бандла.'
