# allocbench

`allocbench` — инструмент для воспроизводимого сравнения аллокаторов памяти (glibc malloc, mimalloc, jemalloc, tcmalloc и др.) на наборе тестов.

Каждая сборка и каждый запуск выполняются в новом изолированном окружении: файловая система собирается из зафиксированного по дате rootfs Ubuntu, сети нет, переменные окружения хоста не видны.
Аллокаторы, тесты и окружения описываются простыми TOML-рецептами: чтобы добавить аллокатор, достаточно добавить файл, менять код или скрипты не нужно.
Docker и bash-скрипты не используются.

## Содержание

1. [Зачем](#зачем)
2. [Как это работает](#как-это-работает)
3. [Архитектура](#архитектура)
4. [Требования](#требования)
5. [Быстрый старт](#быстрый-старт)
6. [Рецепты](#рецепты)
7. [Хранилище](#хранилище)
8. [Прогон и результаты](#прогон-и-результаты)
9. [Команды](#команды)
10. [Разработка](#разработка)

---

## Зачем

Результат сравнения аллокаторов зависит не только от самих аллокаторов, но и от всего, что их окружает:

| Что влияет | Пример |
| :--- | :--- |
| Компилятор и его версия | gcc 13 и gcc 14 по-разному оптимизируют тест |
| glibc | системный malloc (`sys`) — это malloc из glibc хоста; обновили систему — поменялся «системный аллокатор» |
| Версии исходников | `master` аллокатора сегодня и через месяц — разный код |
| Переменные окружения | `MALLOC_ARENA_MAX`, `LD_PRELOAD`, `CFLAGS` из профиля пользователя |
| Состояние машины | файлы и процессы от прошлого запуска |

`allocbench` фиксирует всё, что можно зафиксировать, и записывает то, что зафиксировать нельзя (ядро, процессор).
Цель — чтобы коллега на другой машине получил те же бинарники и сопоставимые цифры.

---

## Как это работает

1. **Рецепт** описывает, что собрать и как запустить. Исходники указываются точным коммитом (`rev`), плавающие версии запрещены.
2. **Окружение** (rootfs) создаётся `mmdebstrap` из снапшота архива Ubuntu на заданную дату. Одна и та же дата даёт одни и те же версии gcc и glibc сегодня и через год.
3. **Песочница** (`bwrap`) создаётся заново на каждое действие. Процесс видит только rootfs (только чтение) и нужные ему каталоги, сети нет.
4. **Хранилище** адресуется хешами входных данных. Если рецепт и исходники не менялись, сборка берётся готовой. Поменялся хотя бы байт — будет пересборка.
5. **Раннер** на C запускает тест через `fork`/`exec`/`wait4` и меряет время, пиковую память и page faults именно процесса теста.

---

## Архитектура

```mermaid
flowchart TB
    Recipes[/"recipes/*.toml"/]
    Plan["recipe + plan<br/>разбор, проверка, хеши,<br/>сборки и ячейки замеров"]

    subgraph BuildStage["allocbench build"]
        direction LR
        Source["source<br/>git · url · путь"] --> Env["env<br/>mmdebstrap → rootfs"] --> BuildBox["песочница сборки<br/>/src → /out"]
    end

    Store[("store/<br/>src · env · build")]

    subgraph RunStage["allocbench run"]
        direction LR
        RunBox["песочница запуска<br/>/alloc · /bench · /runner"] --> Runner["runner (C)<br/>fork/exec → тест"]
    end

    Results[/"results/<br/>manifest · jsonl · csv"/]

    Recipes --> Plan --> BuildStage --> Store --> RunStage --> Results
```

Сборка кладёт результаты в хранилище по хешу входов, прогон берёт их оттуда.
Каждая песочница — и сборки, и запуска — создаётся заново на одно действие: в ней только rootfs окружения (только чтение) и нужные этому действию каталоги.

| Компонент | Где | Что делает |
| :--- | :--- | :--- |
| CLI | `cmd/allocbench`, `internal/cli` | команды `list`, `build`, `shell`, `run`, `lock` |
| Рецепты | `internal/recipe` | разбор TOML, `extends`, проверка полей, входные хеши |
| План | `internal/plan` | порядок сборок с учётом зависимостей, ячейки замеров |
| Хранилище | `internal/store` | адресация по хешу, блокировки, атомарная запись |
| Исходники | `internal/source` | скачивание и проверка исходников, хеш дерева файлов |
| Окружения | `internal/env` | создание rootfs через `mmdebstrap`, безопасная распаковка |
| Песочница | `internal/sandbox` | сборка аргументов `bwrap` |
| Сборка | `internal/build` | зависимости → исходники → окружение → песочница → хранилище |
| Раннер | `runner/runner.c`, `internal/runner` | замер одного запуска, протокол JSON |
| Ячейка | `internal/cell` | песочница запуска, `LD_PRELOAD`, таймаут, проверка вывода |
| Результаты | `internal/results` | `manifest.json`, `results.jsonl`, `results.csv`, сведения о машине |
| Lock-файл | `internal/lock` | хеши окружений и сборок, сверка с `allocbench.lock` |

---

## Требования

- Linux x86_64, ядро 5.3+, с разрешёнными непривилегированными user namespaces
- [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap`)
- [mmdebstrap](https://gitlab.mister-muffin.de/josch/mmdebstrap) и запись пользователя в `/etc/subuid` и `/etc/subgid`
- `git`
- Go 1.22+ для сборки самого `allocbench`

Установка на Ubuntu/Debian:

```bash
sudo apt install bubblewrap mmdebstrap git golang
grep -q "^$USER:" /etc/subuid || echo "$USER:231072:65536" | sudo tee -a /etc/subuid /etc/subgid
```

Root не нужен ни для сборки окружений, ни для сборок, ни для запусков.

---

## Быстрый старт

```bash
git clone git@github.com:SolonenkoNikita/allocbench.git
cd allocbench
make build
```

Посмотреть рецепты:

```bash
$ ./allocbench list
KIND       NAME    VERSION  BUILD ENV  SOURCE                         HASH
env        gcc13   -        -          ubuntu noble 20260101T000000Z  c4d1894efccc
allocator  mi      v1.8.2   gcc13      git b66e3214d8a1               96bc9d605cbb
allocator  sys     -        -          -                              -
bench      cfrac   -        gcc13      git 69c41ed7e641               32b67fc7cfff
tool       runner  -        gcc13      path runner                    7d22fb868ef4
```

Собрать mimalloc и тест cfrac. В первый раз создаётся окружение, это занимает несколько минут:

```bash
$ ./allocbench build mi cfrac sys
fetching source of allocators/mi
creating environment gcc13, this takes a few minutes
building allocators/mi
fetching source of benches/cfrac
building benches/cfrac
RECIPE          STATUS            HASH          OUTPUT
allocators/mi   built             c05b0ff67bbd  /…/store/build/c05b0ff67bbd…
benches/cfrac   built             104a21e78121  /…/store/build/104a21e78121…
allocators/sys  nothing to build  -             -
```

Повторный запуск берёт всё из хранилища (`cached`).

Если снапшот Ubuntu временно недоступен, можно указать обычное зеркало (версии пакетов тогда не будут зафиксированы):

```bash
./allocbench build mi --mirror http://archive.ubuntu.com/ubuntu
```

Открыть оболочку в песочнице сборки, например, чтобы разобраться с упавшей сборкой:

```bash
./allocbench shell mi                      # интерактивный bash
./allocbench shell mi -- cmake --version   # одна команда
./allocbench shell gcc13                   # только окружение
```

Сравнить аллокаторы. `run` сам соберёт всё недостающее и выполнит каждую ячейку в новой песочнице:

```bash
$ ./allocbench run --alloc sys,mi --bench cfrac --repeat 3
host: Intel(R) Core(TM) i5-10310U CPU @ 1.70GHz, 8 CPUs, amd64, kernel 6.14.0-36-generic
running 6 cells, results in results/20261112T200539Z
[1/6] cfrac/sys/t1/r0              ok             8.401s   3.1 MB
[2/6] cfrac/mi/t1/r0               ok             7.615s   3.0 MB
[3/6] cfrac/sys/t1/r1              ok             8.395s   2.9 MB
[4/6] cfrac/mi/t1/r1               ok             7.648s   2.9 MB
[5/6] cfrac/sys/t1/r2              ok             8.305s   3.0 MB
[6/6] cfrac/mi/t1/r2               ok             7.598s   2.9 MB

BENCH  THREADS  ALLOC  OK   MEDIAN  MIN     MAX     RELATIVE  MAX RSS
cfrac  1        sys    3/3  8.395s  8.305s  8.401s  1.00      3.0 MB
cfrac  1        mi     3/3  7.615s  7.598s  7.648s  0.91      2.9 MB
```

`RELATIVE` — медиана времени относительно первого аллокатора в списке: удобно ставить `sys` первым.

---

## Рецепты

Рецепт — TOML-файл в `recipes/<вид>/<имя>.toml`. Имя рецепта — имя файла.
Рецепты проверяются строго: опечатка в имени поля, короткий хеш коммита или ссылка на несуществующее окружение — это ошибка с указанием рецепта, а не тихо проигнорированная настройка.

### Окружение: `recipes/envs/gcc13.toml`

```toml
distro   = "ubuntu"
suite    = "noble"
snapshot = "20260101T000000Z"
packages = ["build-essential", "gcc-13", "g++-13", "cmake", "ninja-build"]
```

### Аллокатор: `recipes/allocators/mi.toml`

```toml
version   = "v1.8.2"
build_env = "gcc13"

[source]
git    = "https://github.com/microsoft/mimalloc"
rev    = "b66e3214d8a104669c2ec05ae91ebc26a8f5ab78"
sha256 = "a27f09204d8273efed245a8ed7093fd062d65b1a14fb0fa121e375b96b5aff3d"

[build]
script = '''
cmake -S /src -B . -G Ninja $CMAKE_FLAGS
ninja -j "$JOBS"
install -Dm755 libmimalloc.so /out/lib/libmimalloc.so
'''
vars = { CMAKE_FLAGS = "-DCMAKE_BUILD_TYPE=Release -DMI_BUILD_TESTS=OFF" }

[use]
preload = "lib/libmimalloc.so"
```

Системный аллокатор — пустой файл `recipes/allocators/sys.toml`: собирать нечего, используется malloc из glibc окружения.

Вариант аллокатора наследует всё от родителя и переопределяет только отличия:

```toml
extends = "mi"

[build]
vars = { CMAKE_FLAGS = "-DCMAKE_BUILD_TYPE=Release -DMI_SECURE=ON" }
```

### Тест: `recipes/benches/cfrac.toml`

```toml
build_env = "gcc13"

[source]
git    = "https://github.com/daanx/mimalloc-bench"
rev    = "69c41ed7e6419fb5d362eb04ba34b850ec89dc8e"
sha256 = "2ff71f5e3b39d6467d199eac07f794e1bf80e140030a6e082fdefd27af022fd2"

[build]
script = '''
cd /src/bench/cfrac
mkdir -p /out/bin
gcc $CFLAGS -o /out/bin/cfrac \
    cfrac.c pops.c pconst.c pio.c \
    pabs.c pneg.c pcmp.c podd.c phalf.c \
    padd.c psub.c pmul.c pdivmod.c psqrt.c ppowmod.c \
    atop.c ptoa.c itop.c utop.c ptou.c errorp.c \
    pfloat.c pidiv.c pimod.c picmp.c \
    primes.c pcfrac.c pgcd.c -lm
'''
vars = { CFLAGS = "-O3 -DNDEBUG -std=gnu89 -DNOMEMOPT=1 -w" }

[run]
cmd     = ["/bench/bin/cfrac", "17545186520507317056371138836327483792789528"]
timeout = "5m"
threads = [1]

[check]
stdout_sha256 = "99eb4f7b4392295adfe8b8d257802f910cdbc1eaf9b8f8785c6b62fc32229ef6"
```

Скрипты пишутся в `'''`: внутри них TOML ничего не экранирует, и `\` в конце строки доходит до shell как перенос.

### Основные поля

| Поле | Где | Описание |
| :--- | :--- | :--- |
| `build_env` | аллокатор, тест, инструмент | окружение, в котором идёт сборка |
| `source.git` + `source.rev` | | репозиторий и полный хеш коммита (40 символов) |
| `source.url` + `source.sha256` | | архив и его хеш |
| `source.path` | | локальный каталог относительно корня проекта |
| `source.sha256` | при `git` | необязательный хеш дерева файлов после checkout |
| `source.patches` | | патчи, применяются через `git apply` |
| `build.script` | | скрипт `sh -eu`; исходники в `/src` (только чтение), рабочий каталог `/build`, результат кладётся в `/out` |
| `build.vars` | | переменные для скрипта |
| `build.deps` | | инструменты из `recipes/tools`; монтируются в `/deps/<имя>`, их `bin` добавляется в `PATH` |
| `use.preload` | аллокатор | путь к библиотеке внутри результата сборки для `LD_PRELOAD` |
| `run.cmd`, `run.timeout`, `run.threads` | тест | команда запуска, таймаут, число потоков (каждое значение — отдельная ячейка) |
| `check.stdout_sha256` | тест | ожидаемый хеш вывода: аллокатор не должен ломать программу |
| `extends` | аллокатор, тест | наследование от другого рецепта того же вида |

В скрипте сборки всегда доступны `JOBS` (флаг `-j`), `SOURCE_DATE_EPOCH` (дата снапшота окружения) и `HOME=/build`.

---

## Хранилище

По умолчанию `./store`, задаётся флагом `--store`.

```
store/
├── src/<ключ>/            исходники
├── env/<хеш>/rootfs/      распакованный rootfs окружения
├── env/<хеш>/env.json     хеш содержимого rootfs и зеркало
├── build/<хеш>/           результаты сборок (то, что скрипт положил в /out)
├── logs/                  логи сборок и создания окружений
├── tmp/                   незавершённые записи
└── locks/                 блокировки
```

- Запись появляется в хранилище только целиком: всё пишется в `tmp/`, затем атомарно переименовывается.
- Упавшая сборка в хранилище не попадает. В сообщении об ошибке выводится конец лога и путь к полному логу.
- Ключ сборки учитывает рецепт после `extends`, окружение, содержимое исходников и патчей, а также ключи зависимостей. Правка локального файла или зависимости вызывает пересборку.
- Хранилище можно удалить целиком: всё пересоздастся при следующем запуске.

---

## Прогон и результаты

Ячейка — один замер: аллокатор × тест × число потоков × номер повтора.
Каждая ячейка выполняется в новой песочнице. Раннер на C запускает тест, передаёт только ему `LD_PRELOAD` и переменные аллокатора и меряет время, пиковую память и page faults.

По умолчанию ячейки чередуются (`sys`, `mi`, `sys`, `mi`, …), чтобы медленный дрейф машины не доставался одному аллокатору.
Упавший тест, таймаут или неверный вывод не прерывают прогон: ячейка получает статус, а команда в конце завершается с ошибкой.

План можно задать флагами или файлом, флаги переопределяют файл:

```toml
allocators = ["sys", "mi"]
benches    = ["cfrac"]
run_env    = "gcc13"
repeat     = 5
order      = "interleaved"
```

```bash
./allocbench run --plan plan.toml
./allocbench run --plan plan.toml --repeat 10
./allocbench run --alloc all --bench all
```

Результаты складываются в `results/<время UTC>/`:

```
results/20261112T200539Z/
├── manifest.json        версия allocbench, план, окружение запуска, хеши всех сборок, параметры машины
├── results.jsonl        одна строка JSON на ячейку
├── results.csv          то же таблицей, для графиков
├── summary.txt          сводка, как в терминале
└── cells/cfrac/mi/t1/r0/
    ├── stdout, stderr   вывод теста
    ├── result.json      замер раннера
    └── sandbox.log      вывод песочницы
```

Поля замера:

| Поле | Описание |
| :--- | :--- |
| `status` | `ok`, `exit code N`, `killed by signal N`, `timeout`, `exec error: …`, `wrong output` |
| `wall_ns` | реальное время выполнения |
| `user_us`, `sys_us` | время CPU в пространстве пользователя и в ядре |
| `max_rss_kb` | пиковая резидентная память |
| `minflt`, `majflt` | page faults без чтения с диска и с чтением |
| `nvcsw`, `nivcsw` | добровольные и принудительные переключения контекста |
| `stdout_sha256` | хеш вывода; сверяется с `check.stdout_sha256` рецепта |

В `manifest.json` записываются ядро, модель CPU, число ядер, объём памяти, режим transparent huge pages, `cpu governor` и `overcommit_memory`: rootfs их не фиксирует, а на результат они влияют.

---

## Команды

| Команда | Описание |
| :--- | :--- |
| `allocbench list` | таблица рецептов с входными хешами |
| `allocbench build <рецепт>...` | собрать рецепты и их зависимости; рецепт — `mi` или `allocators/mi` |
| `allocbench shell <рецепт> [-- команда]` | оболочка в песочнице сборки рецепта или в окружении |
| `allocbench run` | собрать всё нужное плану и выполнить ячейки; флаги `--plan`, `--alloc`, `--bench`, `--run-env`, `--repeat`, `--order`, `--results` |
| `allocbench lock [рецепт]...` | записать хеши окружений и сборок в `allocbench.lock`; с `--check` — сверить с ним |

Общие флаги:

| Флаг | По умолчанию | Описание |
| :--- | :--- | :--- |
| `--recipes` | `recipes` | каталог рецептов |
| `--store` | `store` | каталог хранилища |
| `--mirror` | снапшот из рецепта | другое зеркало пакетов |
| `--proxy` | из `https_proxy` / `http_proxy` | прокси для скачивания пакетов |
| `-v`, `--verbose` | выключен | показывать вывод сборки и `mmdebstrap` |

---

## Разработка

```bash
make build    # собрать ./allocbench
make test     # все тесты
```

Тесты песочницы, сборки и раннера запускают настоящий `bwrap` и компилятор C. Если их нет, такие тесты пропускаются.
Сеть и `mmdebstrap` в тестах не используются: окружение подменяется rootfs хоста.
