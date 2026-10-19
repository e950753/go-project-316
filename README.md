# Парсер сайтов (Go)

[![hexlet-check](https://github.com/e950753/go-project-316/actions/workflows/hexlet-check.yml/badge.svg)](https://github.com/e950753/go-project-316/actions)

Разработайте утилиту для обхода и анализа сайтов

Учебный проект Хекслета: https://ru.hexlet.io/programs/go


## Стек

- Go
- [urfave/cli](https://github.com/urfave/cli)
- [golang.org/x/net/html](https://pkg.go.dev/golang.org/x/net/html)
- `net/http`, `context`, `sync` — сеть и конкурентность

## Установка

<!-- Опишите установку: клонирование, зависимости, переменные окружения -->

```bash
git clone https://github.com/e950753/go-project-316.git
cd go-project-316
make build
make test
```

## Использование

```bash
# через make
make run URL=https://example.com
make run URL=https://example.com ARGS="--depth 2 --workers 8"
 
# или напрямую
bin/hexlet-go-crawler https://example.com
bin/hexlet-go-crawler --depth 2 --workers 8 --rps 5 https://example.com
```

---
https://asciinema.org/a/0BIrqSgDHikBukxG
---

<details>
<summary>Автоматические тесты Хекслета</summary>

Тесты запускаются на каждый коммит. За запуск отвечает файл `.github/workflows/hexlet-check.yml` — не удаляйте и не переименовывайте ни его, ни репозиторий.

</details>

## О Хекслете

[Хекслет](https://ru.hexlet.io/) — школа программирования: авторские программы обучения с практикой, поддержкой наставников и реальными проектами, которые остаются в резюме. Этот репозиторий — один из таких проектов.
