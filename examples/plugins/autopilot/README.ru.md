# Autopilot

Autopilot (`example.autopilot`) — флагманский пример конечных автоматизаций
Serenox. Он показывает в одном реальном плагине команды владельца, watcher
сообщений, атомарное хранилище, постоянные задания, типизированные результаты
Telegram-действий, необязательные согласования через control bot и безопасную
демонстрацию перезапуска процесса.

Это не язык программирования и не спрятанный shell. В определении есть ровно
один триггер и не более 16 известных шагов; циклы, переходы назад, произвольные
URL, код и команды ОС запрещены валидатором.

## Сборка и запуск

Из корня репозитория:

```sh
go test ./examples/plugins/autopilot/...
mkdir -p bin dist
go build -o bin/serenox ./cmd/serenox
bin/serenox plugin build --source examples/plugins/autopilot \
  --output dist/example.autopilot-1.0.0.srnx
bin/serenox plugin verify dist/example.autopilot-1.0.0.srnx
bin/serenox plugin install dist/example.autopilot-1.0.0.srnx
```

`plugin build` сам собирает `bin/autopilot`, заполняет таблицу файлов и создаёт
архив. Сгенерированные бинарник и `.srnx` не следует коммитить.

Первый сценарий в Telegram:

```text
.autopilot demo install
.autopilot demo run
.flow runs demo-reminder
.flow audit
```

Сценарий отправит уведомление, заснёт через постоянное задание Serenox,
возобновится и завершится. Повторная установка demo идемпотентна.

## Команды владельца

```text
.autopilot help | status | pause | resume | cleanup
.autopilot demo install | demo run
.autopilot chaos status
.autopilot chaos fail-job confirm
.autopilot chaos interrupt-run confirm
.autopilot chaos exit-process confirm

.flow list | show <id> | enable <id> | disable <id> | trigger <id>
.flow cancel <run> | retry <run> | approve <run> | reject <run>
.flow runs [workflow] | audit [run]
.flow export <id> | import <строгий-json>
.flow delete <id> confirm
```

Control bot необязателен. При `control_bot_enabled=true` приватная команда
`/autopilot` показывает состояние, а шаг approval присылает кнопки владельцу.
Если бот не подключён, успех не выдумывается: run остаётся в ожидании, решение
принимается через `.flow approve` или `.flow reject`.

## Настройки и границы

| Ключ | По умолчанию | Назначение |
|---|---:|---|
| `chaos_enabled` | `false` | Разрешает только подтверждённые chaos-команды этого процесса. |
| `control_bot_enabled` | `false` | Включает UI согласований через подключённого бота. |
| `http_enabled` | `false` | Включает каталог подписанных HTTP-коннекторов. |

В пакете есть только read-only alias `demo-status` для точного origin
`https://example.com`. Пользователь не передаёт произвольный URL. Токены в
workflow не поддерживаются и не сохраняются.

Жёсткие пределы: 32 workflow, 16 шагов, 32 перехода, 64 run, 256 audit-событий,
32 KiB на определение, 16 KiB на контекст, 4096 байт на итоговый текст и 256
байт на регулярное выражение. Одновременно у run может быть только одно
ожидаемое задание и одно согласование. У процесса два SDK worker и очередь 32.

## Что происходит при сбое

Хранилище — один документ schema v1 в namespace плагина; запись атомарна.
Telegram, HTTP и storage не являются общей транзакцией. Шаг проходит дальше
только после типизированного `success`. Результат `outcome_unknown` не
повторяется автоматически: отправка могла успеть выполниться.

Для демонстрации включите `chaos_enabled`, затем:

```text
.autopilot chaos exit-process confirm
.ping
# дождитесь рестарта supervisor
.flow runs chaos-recovery
.flow audit
```

Перед выходом с кодом 23 плагин сохраняет активный run. Он не останавливает и
не сигналит daemon, Telegram transport или другие плагины, поэтому `.ping`
остаётся доступным. После рестарта первая команда/job загружает состояние и
помечает run как `interrupted`. Восстановление ленивое: lifecycle v1 пока не
может вызывать host RPC до запуска dispatch-loop.

Подпись `.srnx` и RPC permissions не являются sandbox ОС. Нативный плагин —
обычный отдельный процесс под учётной записью daemon; демонстрация доказывает
supervision, конечные очереди, типизированные разрешения, постоянные jobs и
явное восстановление, а не невозможность написать похожую бизнес-логику в
Hikka.

Полная схема, таблица режимов и диагностика: [docs/autopilot.md](../../../docs/autopilot.md).
