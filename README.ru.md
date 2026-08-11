<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <img src="assets/logo.svg" width="140" alt="kartograf">
  </picture>
</p>

<h1 align="center">kartograf</h1>

<p align="center">[English version](README.md)</p>

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
Пошаговые инструкции для AI-агентов лежат в
[docs/install-prompt.md](docs/install-prompt.md) — вставьте в Claude
Code (или любого агента с доступом к шеллу) внутри проекта, который
хотите проиндексировать:

```text
Fetch https://raw.githubusercontent.com/dev-manul/kartograf/master/docs/install-prompt.md
and follow the instructions to install the kartograf MCP server for
this project.
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
kartograf self-update                       # обновиться до последнего релиза
```

Регистрация в Claude Code:

```sh
claude mcp add kartograf -- kartograf serve /path/to/project
```

Для Cursor и других stdio MCP-клиентов (обязателен `"type": "stdio"`)
см. [docs/cursor.md](docs/cursor.md).

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
На каждом ребре есть `source`: `ast` (file-local извлечение),
`phpstan` или `go-types` (слой точности).

Что работает без слоя точности:

| Тулза | Только AST | с `enrich` |
|------|----------|---------------|
| `search_symbols`, `file_outline`, `get_symbol` | ✅ | ✅ |
| `class_hierarchy` | ✅ PHP/TS; в Go нет `implements` | ✅ |
| `find_references` | ⚠️ частично для динамики | ✅ |
| `get_callees` | ⚠️ мало resolved в нетипизированном PHP | ✅ |
| `get_callers` (PHP интерфейсы/DI) | ❌ почти всё эвристика | ✅ |

Для PHP-проектов `kartograf enrich php` — фактически обязателен для
графа вызовов, а не опциональная фича: `serve` предупреждает, если
его нет.

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

### Enrich в Docker / CI

Если PHP живёт только в контейнере: сгенерируйте конфиг локально,
запустите PHPStan там, где есть PHP, и импортируйте результат:

```sh
kartograf enrich php --skip-run /path/to/project   # только scaffold .kartograf/phpstan/
docker compose exec app php vendor/bin/phpstan analyse \
  -c .kartograf/phpstan/kartograf.neon \
  --autoload-file .kartograf/phpstan/KartografExportRule.php \
  --error-format json --memory-limit 4G > /tmp/phpstan.json
kartograf enrich php --from-json /tmp/phpstan.json /path/to/project
```

Контейнерные пути в JSONL маппятся на индексированные файлы
автоматически (по самому длинному суффиксу).

Повторные прогоны инкрементальны бесплатно: рёбра едут через result
cache PHPStan, поэтому переанализируются только изменённые файлы
(~20 с на тёплом кэше 79k-файлового монолита против минут с нуля).
JSONL перезаписывается целиком — семантика replace, без слияния.

Коммитьте JSONL, чтобы шарить разрезолвленный граф вызовов с командой
(и CI-агентами), либо добавьте `.kartograf/` в `.gitignore` и
перегоняйте `enrich` после больших изменений — работает и так и так,
`index`/`serve` реимпортируют файл при изменении.

### Ожидания по скорости

grep выигрывает на сыром тексте; kartograf — на семантике графа:

| Задача | grep/rg | kartograf |
|--------|---------|-----------|
| поиск текста | ~0.06s | ~мс (`search_symbols`, тёплый) |
| использования класса | сотни шумных текстовых совпадений | типизированные рёбра с kind и резолвом |
| кто вызывает метод | нереально | `get_callers` ~мс (PHP требует enrich) |
| первый ответ после старта MCP | — | <1 с на 80k-файловой репе (рефреш индекса в фоне) |

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
