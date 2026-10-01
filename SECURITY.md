# Security

[Русский текст — ниже](#безопасность)

## Reporting a vulnerability

Please report security problems **privately** through GitHub:
**Security → Report a vulnerability** in this repository
([direct link](https://github.com/zaevlad/vpsfocus-server/security/advisories/new)).
Do not open a public issue for them.

We answer within 7 days. Please give us a reasonable time to ship a fix
before you publish details: a fix reaches users' servers only when they
update the monitoring from the app.

In scope: everything in this repository — the installer bundle, the scripts
the app runs over SSH, the agent and our Caddy image.

## The release key

Every bundle is signed with the release key, fingerprint
**`337F561861A23810`** (minisign key id), public half in `keys/release.pub`.
The same fingerprint is in [README.md](README.md) and on
[vpsfocus.xyz/en/server-code](https://vpsfocus.xyz/en/server-code).

If the key is ever lost or exposed, we will: make a new key; ship a new app
version that trusts only the new key (the app knows exactly one key, so
updating the app is required); re-sign the current bundle; and record here
which key was revoked, from which date and why.

---

# Безопасность

## Как сообщить об уязвимости

Сообщайте **не публично**, через GitHub: **Security → Report a vulnerability**
в этом репозитории
([прямая ссылка](https://github.com/zaevlad/vpsfocus-server/security/advisories/new)).
Публичную issue об уязвимости не открывайте.

Отвечаем в течение 7 дней. Дайте нам разумное время выпустить исправление,
прежде чем публиковать подробности: до серверов пользователей оно доходит,
только когда они обновят мониторинг из приложения.

Что сюда относится: всё в этом репозитории — установочные файлы, скрипты,
которые приложение выполняет по SSH, агент и наш образ Caddy.

## Ключ выпуска

Каждая версия подписана ключом выпуска, отпечаток **`337F561861A23810`**
(идентификатор ключа minisign), публичная половина — `keys/release.pub`. Тот
же отпечаток — в [README.ru.md](README.ru.md) и на
[vpsfocus.xyz/ru/server-code](https://vpsfocus.xyz/ru/server-code).

Если ключ потеряется или утечёт, мы: заводим новый ключ; выпускаем версию
приложения, которая доверяет только новому (приложение знает ровно один ключ,
поэтому обновить приложение обязательно); заново подписываем текущую версию
установочных файлов; и записываем здесь, какой ключ отозван, с какой даты и
почему.
