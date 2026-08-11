# kartograf

[English version](README.md)

Строит карту кода проекта (символы, ссылки, граф вызовов) и отдаёт её
AI-агентам через MCP. Парсинг — tree-sitter, ядро языконезависимое;
реализованы адаптеры **PHP**, **Go** и **TypeScript/JavaScript**.

## Возможности

- Извлечение символов: классы/интерфейсы/трейты/енумы (PHP),
  структуры/интерфейсы/type alias (Go), классы/интерфейсы/енумы/type
  alias и const-стрелочные компоненты (TS/JS, включая TSX/JSX), методы,
  свойства (включая промоутнутые и parameter properties), константы,
  функции, докблоки.
- Рёбра ссылок с резолвом на этапе извлечения по file-local знанию:
  инстанцирования, статические и инстансные вызовы (`$this->`,
  `self::`, `parent::`, типизированные свойства и параметры, one-hop
  через поля структур в Go), доступ к константам, type hints,
  `instanceof`, атрибуты, наследование и трейты/embedding. Рендер
  JSX-компонента — ребро вызова; импорты TS резолвятся по
  относительным путям и именам workspace-пакетов (package.json).
- Инкрементальный индекс в SQLite + FTS5: stat-фастпас (mtime+size),
  sha256 как источник истины — переключение веток реиндексирует только
  реальный дифф. Vendor индексируется неглубоко (декларации +
  иерархия, без графа вызовов).
- MCP-сервер (stdio) с графовыми тулзами; callers с учётом иерархии
  классов через рекурсивные CTE.
- Опциональный слой точности (`kartograf enrich`): полный
  тайп-инференс поверх file-local AST-эвристик.

## Быстрая установка (руками AI-агента)

Готовые бинари под macOS (Intel/Apple Silicon) и Linux (amd64/arm64)
прикреплены к каждому
[релизу](https://github.com/dev-manul/kartograf/releases/latest).
Вставьте этот промпт в Claude Code (или любого агента с доступом к
шеллу) внутри проекта, который хотите проиндексировать:

```text
Install the kartograf MCP server for this project:
1. Detect my OS and architecture (uname -s / uname -m) and download the
   matching asset from
   https://github.com/dev-manul/kartograf/releases/latest/download/kartograf-<os>-<arch>
   where <os>-<arch> is one of: darwin-arm64, darwin-amd64, linux-amd64,
   linux-arm64.
2. Install it to ~/.local/bin/kartograf (create the directory if
   needed), chmod +x. On macOS also run:
   xattr -d com.apple.quarantine ~/.local/bin/kartograf || true
3. Build the index: ~/.local/bin/kartograf index . — and show me the
   summary line it prints (files/symbols).
4. Register the MCP server for this project:
   claude mcp add kartograf -- ~/.local/bin/kartograf serve "$PWD"
5. Tell me to restart the session so the kartograf tools appear:
   search_symbols, get_symbol, find_references, get_callers,
   get_callees, class_hierarchy, file_outline.
```

## Сборка из исходников

Сборка требует build-тег `sqlite_fts5` (FTS5 в mattn/go-sqlite3);
без тега компиляция намеренно падает с понятной ошибкой. Проще через
Makefile:

```sh
make install        # go install -tags sqlite_fts5 ./cmd/kartograf
make check          # vet + test + fmt + build

kartograf index [root]                      # построить/обновить индекс
kartograf index --rebuild                   # с нуля
kartograf serve [root]                      # MCP-сервер на stdio (сам доиндексирует на старте)
kartograf outline path/to/File.php          # символы файла
kartograf outline --json path/to/File.php   # полный FileIndex в JSON
```

Регистрация в Claude Code:

```sh
claude mcp add kartograf -- kartograf serve /path/to/project
```

Индекс лежит в кэше пользователя
(`~/Library/Caches/kartograf/<проект>-<hash>/index.db` на macOS,
`~/.cache/...` на Linux) — производный артефакт, в гит не коммитится.
При смене версии схемы база молча пересобирается.

Ориентиры на PHP-монолите (~79k файлов с vendor, ~885k символов):
холодный индекс ~19 с (bulk-режим: батчевые вставки, индексы и FTS
строятся один раз после заливки), тёплый прогон ~1.5 с.

## MCP-тулзы

| Тулза | Что делает |
|---|---|
| `search_symbols` | FTS по именам/FQN/докблокам (понимает camelCase), фильтр по kind |
| `get_symbol` | Декларация по FQN (или хвосту имени): сигнатура, док, члены, исходник |
| `find_references` | Все ссылки на символ: вызовы, new, type hints, instanceof, константы |
| `get_callers` | Кто вызывает метод/функцию; учитывается иерархия классов |
| `get_callees` | Что вызывает/инстанцирует символ |
| `class_hierarchy` | Транзитивные предки и потомки (реализации интерфейса) |
| `file_outline` | Символы файла |

Рёбра с `resolved=false` — эвристика (вызов через `parent::`,
выведенный тип получателя, глобальный фолбэк функций); точные рёбра
резолвятся по правилам языка из карты импортов и неймспейса файла.

## Слой точности

`kartograf enrich` добавляет рёбра от инструментов полного
тайп-инференса. Они хранятся в `.kartograf/enrich.<source>.jsonl` в
корне проекта (коммитить или игнорировать — на ваше усмотрение) и
автоматически реимпортируются `index`/`serve` при изменении файла;
удаление файла убирает его рёбра.

- `kartograf enrich go` — go/packages + go/types в процессе: точные
  вызовы (интерфейсные, через поля из других файлов) и структурные
  `implements`-рёбра (иерархию интерфейсов Go из file-local AST не
  получить в принципе).
- `kartograf enrich php` — генерирует правило PHPStan в
  `.kartograf/phpstan/` и запускает `vendor/bin/phpstan` с конфигом
  проекта. Рёбра едут как псевдо-ошибки с identifier `kartograf.edge`
  через JSON-вывод — result cache PHPStan делает повторные прогоны
  инкрементальными. Разрешает вызовы через нетипизированные свойства
  (тип выводится из конструктора). Если php нет локально — запустите
  phpstan где угодно и импортируйте: `kartograf enrich import <file>
  --source phpstan` (контейнерные пути маппятся автоматически).

## Конфиг проекта — `.kartograf.yml` (опционально)

```yaml
include: []        # директории для индексации (по умолчанию весь корень)
exclude: []        # доп. gitignore-паттерны
vendor: index      # index (по умолчанию, с пометкой vendor) | skip
```

`.gitignore` проекта уважается; vendor/node_modules индексируются в
обход gitignore и помечаются флагом vendor.

## Архитектура

- `internal/core/model` — языконезависимая модель: `Symbol`, `Import`,
  `Ref`, `FileIndex`. ID символа глобален и детерминирован:
  `php:App\Service\Foo::bar()`, `go:module/pkg.Type.Method()`,
  `ts:src/api/client#ApiClient.get()` (модуль = путь файла).
- `internal/core/lang` — контракт языкового адаптера + реестр.
- `internal/core/indexer` — gitignore-aware обход, воркер-пул,
  детект изменений.
- `internal/core/store` — схема SQLite, bulk/инкрементальный writer,
  FTS.
- `internal/core/query` — чтение: поиск, lookups, обходы графа.
- `internal/lang/php`, `internal/lang/golang`, `internal/lang/ts` —
  tree-sitter адаптеры.
- `internal/enrich` — обогащение go/types и PHPStan.
- `internal/mcpserver` — MCP-тулзы поверх query-движка.

Файлы с синтаксическими ошибками парсятся best-effort и помечаются
`hasErrors` (error-recovery у tree-sitter).

## Отладка грамматик

```sh
kartograf parse-tree file.php   # сырой CST tree-sitter (скрытая команда)
```
