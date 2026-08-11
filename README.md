# kartograf

Строит карту кода проекта (символы, ссылки, граф вызовов) и отдаёт её
AI-агентам через MCP. Парсинг — tree-sitter, ядро языконезависимое;
сейчас поддержан PHP, дальше — TS/JS и Go.

## Статус

Фаза 1 (извлечение PHP-символов) и фаза 2 (инкрементальный индексатор,
SQLite + FTS5) готовы.

Дорожная карта: резолв имён и рёбра (extends/implements/calls) →
MCP-сервер (stdio) → watch-режим → опциональный слой точности на PHPStan.

## Использование

```sh
go build ./cmd/kartograf

kartograf index [root]                      # построить/обновить индекс
kartograf index --rebuild                   # с нуля
kartograf outline path/to/File.php          # символы файла
kartograf outline --json path/to/File.php   # полный FileIndex в JSON
```

Индекс лежит в кэше пользователя (`~/Library/Caches/kartograf/<проект>-<hash>/index.db`
на macOS, `~/.cache/...` на Linux) — это производный артефакт, в гит не
коммитится. Инкрементальность: stat-фастпас по mtime+size, при
расхождении — сверка sha256 контента; при смене версии схемы база
молча пересобирается.

Ориентиры на монолите api (~79k PHP-файлов с vendor, ~885k символов):
холодный индекс ~75 с, тёплый прогон ~1.5 с.

### Конфиг проекта — `.kartograf.yml` (опционально)

```yaml
include: []        # директории для индексации (по умолчанию весь корень)
exclude: []        # доп. gitignore-паттерны
vendor: index      # index (по умолчанию, с пометкой vendor) | skip
```

`.gitignore` проекта уважается; vendor/node_modules индексируются в
обход gitignore и помечаются флагом vendor.

## Архитектура

- `internal/core/model` — языконезависимая модель: `Symbol`, `Import`,
  `TypeRel`, `FileIndex`. ID символа глобален и детерминирован:
  `php:App\Service\Foo::bar()`.
- `internal/core/lang` — контракт языкового адаптера + реестр.
- `internal/lang/php` — PHP-адаптер на tree-sitter-php: классы,
  интерфейсы, трейты, енумы, методы, свойства (включая промоутнутые в
  конструкторе), константы, функции, `use`-импорты (включая групповые и
  алиасы), факты наследования (extends/implements/use trait).

Файлы с синтаксическими ошибками парсятся best-effort и помечаются
`hasErrors` (error-recovery у tree-sitter).

## Отладка грамматик

```sh
kartograf parse-tree file.php   # сырой CST tree-sitter (скрытая команда)
```
