# kartograf

Строит карту кода проекта (символы, ссылки, граф вызовов) и отдаёт её
AI-агентам через MCP. Парсинг — tree-sitter, ядро языконезависимое;
сейчас поддержан PHP, дальше — TS/JS и Go.

## Статус

Фаза 1: извлечение символов из PHP-файлов, команда `outline`.

Дорожная карта: индексатор всего проекта (SQLite + FTS5, инкрементально
по content-hash) → резолв имён и рёбра (extends/implements/calls) →
MCP-сервер (stdio) → watch-режим → опциональный слой точности на PHPStan.

## Использование

```sh
go build ./cmd/kartograf

kartograf outline path/to/File.php          # символы файла
kartograf outline --json path/to/File.php   # полный FileIndex в JSON
```

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
