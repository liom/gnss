# GNSS Probe — Техническое задание
**Версия 3.0 · Поддержка Quectel LTE-модемов**

| Параметр | Значение |
|---|---|
| Проект | GNSS Probe |
| Версия | 3.0 |
| Дата обновления | Март 2026 |

---

## 1. Обзор и архитектурный контекст

Проба предназначена для сбора данных GNSS-приёмника на мониторинговых агентах, работающих под управлением HashiCorp Nomad. Каждый запуск — короткоживущий контейнер, который пишет один JSON-результат в stdout и завершается.

```
Nomad periodic scheduler
       │  (каждые 60 минут)
       ▼
 gnss-probe container
       │
       ├── /dev/ttyUSB* (явный путь или auto-detected)
       │        │
       │   NMEA sentences (GGA + RMC + TXT)
       │
       ├── stdout → JSON result → Vector → centralised storage
       └── stderr → diagnostics, warnings, debug → Vector → logs
```

### 1.1. Жизненный цикл пробы

- Старт контейнера
- Определение устройства: явный путь или USB auto-scan
- Детектирование типа старта (hot / warm / cold) по GNTXT / прогрессии спутников
- Выбор таймаута фикса в зависимости от типа старта и наличия VBAT
- Подключение к GNSS-приёмнику по serial с retry-логикой
- Сбор NMEA-предложений в течение GPS_READ_DURATION после фикса
- Парсинг GGA + RMC; аккумуляция данных
- Эмиссия структурированного JSON в stdout
- Выход с соответствующим exit code

---

## 2. Коды завершения (Exit Codes)

Все exit codes определены явно. Nomad и внешние системы мониторинга должны реагировать на эти коды:

| Код | Константа | Условие | JSON в stdout |
|---|---|---|---|
| 0 | ExitSuccess | Фикс получен, данные собраны | ✅ Полный результат |
| 1 | ExitNoFix | Таймаут без фикса | ✅ Частичный (fix: false) |
| 2 | ExitDeviceError | Serial порт недоступен или ошибка чтения | ✅ ErrorResult |
| 3 | ExitConfigError | Неверные / отсутствующие ENV переменные | ✅ ErrorResult |

> **⚠️ ВАЖНО:** При exit code 1 (нет фикса) JSON с `fix: false` всё равно эмитируется в stdout. Это позволяет downstream-потребителям получить диагностическую информацию (число спутников, start_type) даже при неудаче. Поле `error` в JSON отсутствует — только `fix: false`.

---

## 3. Холодный, тёплый и горячий старт

### 3.1. Влияние на таймауты

Поскольку стационарные точки запускают пробу раз в час, тип старта критически зависит от наличия battery backup (VBAT) в приёмнике:

| Тип приёмника | Интервал | Тип старта | Рекомендуемый таймаут |
|---|---|---|---|
| Без VBAT (CH340, CP210x, generic) | 1 час | Cold (всегда) | 60–180 сек |
| С VBAT (u-blox NEO-M8+) | 1 час | Hot / Warm | 10–30 сек |
| С VBAT + кэш позиции | 1 час | Hot гарантированно | 5–15 сек |
| Quectel EC25 без XTRA | 1 час | Cold | 60–150 сек |
| Quectel EC25 с XTRA | 1 час | Hot (XTRA через LTE) | 2–10 сек |

> **🔋 VBAT:** Проба автоматически определяет наличие VBAT по VID/PID из whitelist. u-blox NEO-M8 и NEO-9 имеют VBAT — при интервале 1 час всегда hot start. Дешёвые модули на CH340/CP210x — без VBAT, каждый запуск = cold start.

### 3.2. Детектирование типа старта

Проба определяет тип старта следующими методами (в порядке приоритета):

1. **GNTXT / GPTXT предложения:** u-blox и ряд других чипов явно пишут `COLD START` / `WARM START` / `HOT START` в первые 2 секунды
2. **VID/PID + флаг has_vbat из whitelist:** если VBAT есть и последний фикс был < 4 часов назад (по кэшу `GPS_CACHE_PATH`) — hot start; если VBAT отсутствует — cold start
3. **Прогрессия числа спутников:** быстрый рост до > 4 спутников за < 10 секунд = hot start
4. **Fallback:** если `GPS_ASSUME_COLD_START=true` или тип не определён — всегда cold

```
// Детектирование по $GNTXT / $GPTXT
$GNTXT,01,01,07,COLD START*4B   → start_type = "cold"
$GNTXT,01,01,07,WARM START*17   → start_type = "warm"
$GNTXT,01,01,07,HOT START*20    → start_type = "hot"
```

### 3.3. Логика выбора таймаута

```bash
# Рекомендуемые значения для стационарных точек, интервал 1 час

# Приёмник без VBAT (CH340, CP210x, generic USB-UART)
GPS_FIX_TIMEOUT_COLD=90s
GPS_ASSUME_COLD_START=true

# Приёмник с VBAT (u-blox NEO-M8, NEO-9)
GPS_FIX_TIMEOUT_HOT=10s
GPS_FIX_TIMEOUT_WARM=45s
GPS_FIX_TIMEOUT_COLD=120s

# После кэша последней позиции
GPS_FIX_TIMEOUT_HOT=8s
```

### 3.4. Кэш последней позиции

При успешном фиксе проба сохраняет позицию в файл на хосте. При следующем запуске — читает её и передаёт приёмнику через команду, превращая cold start в assisted warm start:

```
// Монтируется как volume в Nomad job
/var/lib/gnss-probe/last_fix.json

{
  "lat": 52.3676,
  "lon": 4.9041,
  "alt": 12.3,
  "timestamp": 1710003600,
  "hdop": 0.8
}
```

Команды для разных чипов:
- u-blox NEO: UBX-AID-INI или `$PUBX,04` для установки времени
- MediaTek MT33xx: `$PMTK740` (позиция) + `$PMTK741` (время)
- Generic: проба просто передаёт данные если чип поддерживает `$PSRF150`

---

## 4. USB Auto-Detection

### 4.1. Алгоритм автоопределения

Если `GPS_DEVICE=auto` или `GPS_AUTO_SCAN=true`, проба выполняет:

1. Glob `/dev/ttyUSB*` и `/dev/ttyACM*`
2. Чтение VID/PID из Linux sysfs (`/sys/class/tty/<dev>/device/../idVendor`)
3. Сортировка кандидатов по приоритету из whitelist
4. Dedicated GNSS чипы и модемы (priority > 80): добавляются без NMEA-пробы
5. Generic UART мосты (priority ≤ 80): открывается порт, ожидаются NMEA-строки в пределах `GPS_SCAN_TIMEOUT`
6. Выбирается первый подтверждённый кандидат
7. Если VID/PID не читается из sysfs — устройство пропускается с WARNING в stderr

### 4.2. Whitelist VID/PID

| Производитель | Чип | VID | PID | Приоритет | Примечание |
|---|---|---|---|---|---|
| u-blox | NEO-6M | 1546 | 01a7 | 100 ★★★ | Dedicated GNSS — NMEA probe не нужна |
| u-blox | NEO-M8 | 1546 | 01a8 | 100 ★★★ | Dedicated GNSS — NMEA probe не нужна |
| u-blox | NEO-9 | 1546 | 01a9 | 100 ★★★ | Dedicated GNSS — NMEA probe не нужна |
| MediaTek | MT3329/MT3339 | 0e8d | 3329 | 90 ★★★ | Dedicated GNSS — NMEA probe не нужна |
| **Quectel** | **EC25** | **2C7C** | **0125** | **95 ★★★** | **LTE-модем — modem mode (AT+QGPS)** |
| **Quectel** | **EC200A** | **2C7C** | **6005** | **95 ★★★** | **LTE-модем — modem mode (AT+QGPS)** |
| **Quectel** | **EC200U** | **2C7C** | **6001** | **95 ★★★** | **LTE-модем — modem mode (AT+QGPS)** |
| **Quectel** | **EC200T** | **2C7C** | **6002** | **95 ★★★** | **LTE-модем — modem mode (AT+QGPS)** |
| **Quectel** | **EM05** | **2C7C** | **030E** | **95 ★★★** | **LTE-модем — modem mode (AT+QGPS)** |
| Silicon Labs | CP210x | 10c4 | ea60 | 60 ★★ | Generic UART — требует NMEA probe |
| FTDI | FT232R | 0403 | 6001 | 50 ★★ | Generic UART — требует NMEA probe |
| Prolific | PL2303 | 067b | 2303 | 50 ★★ | Generic UART — требует NMEA probe |
| QinHeng | CH340/CH343 | 1a86 | 7523 | 40 ★ | Generic UART — требует NMEA probe |

> **ℹ️ FTDI / CH340:** FTDI FT232R и QinHeng CH340 — самые распространённые USB-UART мосты. Их используют и GNSS-модули, и другие устройства. Без NMEA-пробы нельзя отличить GNSS от Arduino. Проба всегда выполняет проверку для этих чипов.

### 4.3. ENV-переменные auto-scan

| Переменная | По умолчанию | Описание |
|---|---|---|
| `GPS_DEVICE=auto` | — | Включает auto-scan вместо явного пути |
| `GPS_AUTO_SCAN` | false | Принудительный скан поверх GPS_DEVICE |
| `GPS_SCAN_TIMEOUT` | 3s | Таймаут проверки одного кандидата |

---

## 5. Требования к GNSS

### 5.1. Поддерживаемые системы

- GPS (талкер `GP`)
- GLONASS (талкеры `GL` и `GN`)
- Galileo (талкеры `GA` и `GN`)
- BeiDou (талкер `PQ` для EC25; `BD`/`GB` для других чипов)
- Multi-constellation: определяется по совокупности талкеров, а **не** по единственному `GN`

### 5.2. Поведение талкеров NMEA: u-blox vs Quectel EC25

Критически важное отличие, влияющее на логику парсера:

| Предложение | Тип | u-blox NEO-M8/M9 (multi-constel) | Quectel EC25 (multi-constel) |
|---|---|---|---|
| GGA | Позиция + фикс | `$GNGGA` — талкер всегда GN | ⚠️ `$GPGGA` — талкер **всегда GP** |
| RMC | Дата + время + скорость | `$GNRMC` — талкер всегда GN | ⚠️ `$GPRMC` — талкер **всегда GP** |
| GSA | Активные спутники + DOP | `$GNGSA` (несколько, по одному на систему) | `$GPGSA` + `$GNGSA` |
| GSV GPS | Спутники GPS в зоне | `$GPGSV` | `$GPGSV` |
| GSV GLONASS | Спутники GLONASS в зоне | `$GLGSV` | ✅ `$GLGSV` — признак GLONASS |
| GSV Galileo | Спутники Galileo в зоне | `$GAGSV` | ✅ `$GAGSV` — признак Galileo |
| GSV BeiDou | Спутники BeiDou в зоне | `$GBGSV` | ✅ `$PQGSV` — признак BeiDou |
| VTG | Курс и скорость | `$GNVTG` | `$GPVTG` |
| GNS | Positioning system fix | `$GNGNS` | `$GNGNS` |
| TXT | Старт тип, статус антенны | `$GNTXT` | `$GPTXT` |

> **⚠️ Quectel EC25: GGA и RMC всегда GP**
>
> По официальной документации Quectel (EC25&EC21 GNSS AT Commands Manual v1.1, раздел 1.2): GGA и RMC предложения у EC25 используют талкер `GP` даже при активном GLONASS/BeiDou. Это **не баг прошивки** — это задокументированное поведение. u-blox переключает GGA/RMC на `GN` при multi-constellation. EC25 — никогда. Парсер пробы **НЕ должен** ориентироваться на талкер GGA/RMC для определения multi-constellation.

### 5.3. Правила парсинга для EC25 (modem mode)

| Параметр | Источник данных | Логика |
|---|---|---|
| Координаты (lat/lon) | `$GPGGA` поля 2–5 | Всегда GP, парсить напрямую |
| Фикс получен | `$GPGGA` поле 6 | 0 = нет фикса, 1 = GPS fix, 2 = DGPS |
| Число спутников в фиксе | `$GPGGA` поле 7 | Только GPS спутники в фиксе |
| Высота | `$GPGGA` поле 9 | Высота над уровнем моря, метры |
| HDOP | `$GPGGA` поле 8 | Horizontal Dilution of Precision |
| Время UTC | `$GPRMC` поле 1 + поле 9 | HHMMSS.sss + DDMMYY |
| Скорость | `$GPRMC` поле 7 | Узлы, конвертировать в км/ч |
| Курс | `$GPRMC` поле 8 | Градусы от севера |
| GLONASS активен | Наличие `$GLGSV` в потоке | Если `$GLGSV` есть → GLONASS виден |
| Galileo активен | Наличие `$GAGSV` в потоке | Если `$GAGSV` есть → Galileo виден |
| BeiDou активен | Наличие `$PQGSV` в потоке | EC25 использует PQ prefix для BeiDou |
| `provider = "gps"` | Только `$GPGSV`, нет других GSV | Только GPS |
| `provider = "mixed"` | Есть `$GLGSV`, `$GAGSV` или `$PQGSV` | Независимо от талкера GGA |
| Число спутников всего | Сумма полей 3 из всех GSV | `$GPGSV` + `$GLGSV` + `$GAGSV` + `$PQGSV` |
| `start_type` | `$GPTXT` содержит текст старта | EC25 пишет в GPTXT, не в GNTXT |

### 5.4. Универсальная логика определения provider

Применяется для обоих режимов (serial и modem). Приоритет правил сверху вниз:

```go
// Псевдокод для определения provider

// Шаг 1: собрать уникальные talker ID из ВСЕХ принятых предложений
talkers := map[string]bool{}
for each sentence {
    talkers[sentence.TalkerID] = true  // "GP", "GL", "GN", "GA", "PQ" и т.д.
}

// Шаг 2: собрать уникальные типы GSV
gsvTypes := map[string]bool{}
for each GSV sentence {
    gsvTypes[sentence.TalkerID] = true  // "GP", "GL", "GA", "PQ"
}

// Шаг 3: определить provider
switch {
    case gsvTypes["GL"] || gsvTypes["GA"] || gsvTypes["PQ"]:
        provider = "mixed"   // мультиконстелляция
    case talkers["GN"] && !gsvTypes["GP"]:
        provider = "mixed"   // u-blox стиль: только GN талкер
    case talkers["GL"] && !gsvTypes["GP"]:
        provider = "glonass"
    default:
        provider = "gps"
}

// ⚠️ НЕ делать так (ломается на EC25):
// if talker(GGA) == "GN" { provider = "mixed" }  ← НЕВЕРНО
```

---

## 6. Конфигурация (ENV Variables)

### 6.1. Обязательные переменные

| Переменная | Пример | Описание |
|---|---|---|
| `GPS_DEVICE` | `/dev/ttyUSB0` или `auto` | Путь к устройству или режим auto-scan |
| `GPS_BAUDRATE` | `9600` | Скорость порта (9600 для простых приёмников, 115200 для Quectel) |
| `GPS_READ_DURATION` | `5s` | Время сбора NMEA после получения фикса |
| `GPS_FIX_TIMEOUT` | `60s` | Общий таймаут (если HOT/WARM/COLD не заданы) |

### 6.2. Опциональные переменные

| Переменная | По умолчанию | Описание |
|---|---|---|
| `AGENT_ID` | — | Идентификатор агента (добавляется в JSON) |
| `GPS_MIN_SATELLITES` | `4` | Минимум спутников для валидного фикса |
| `GPS_DEBUG_NMEA` | `false` | Вывод сырых NMEA-предложений в stderr |
| `GPS_FIX_TIMEOUT_HOT` | — | Таймаут для hot start |
| `GPS_FIX_TIMEOUT_WARM` | — | Таймаут для warm start |
| `GPS_FIX_TIMEOUT_COLD` | — | Таймаут для cold start |
| `GPS_ASSUME_COLD_START` | `false` | Принудительный cold-таймаут для приёмников без VBAT |
| `GPS_AUTO_SCAN` | `false` | Включить auto-scan поверх GPS_DEVICE |
| `GPS_SCAN_TIMEOUT` | `3s` | Таймаут NMEA-пробы одного кандидата |
| `GPS_CACHE_PATH` | `/var/lib/gnss-probe/last_fix.json` | Путь к файлу кэша позиции |
| `GPS_MODE` | `auto` | `serial` / `modem` / `auto` — выбор режима работы |
| `GPS_MODEM_AT_PORT` | — | Явный путь к AT-порту модема |
| `GPS_MODEM_NMEA_PORT` | — | Явный путь к NMEA-порту модема |
| `GPS_MODEM_BAUD` | `115200` | Скорость для AT и NMEA портов модема |
| `GPS_MODEM_INIT_TIMEOUT` | `5s` | Таймаут ответа на AT-команды инициализации |
| `GPS_XTRA_ENABLE` | `false` | Включить XTRA assisted GPS (требует интернет) |
| `GPS_XTRA_TIME_SYNC` | `true` | Передавать текущее время через AT+QGPSXTRATIME |

> **⚠️ GPS_FIX_TIMEOUT:** Используется если типизированные таймауты (HOT/WARM/COLD) не заданы. Рекомендуется всегда задавать все три для стационарных точек с известным типом приёмника.

---

## 7. Выходной формат JSON

### 7.1. Полный список полей

| Поле | Тип | Описание | Присутствует |
|---|---|---|---|
| `probe` | string | Всегда `"gnss"` | Всегда |
| `timestamp` | int64 | Unix timestamp начала запуска | Всегда |
| `agent_hostname` | string | Hostname агента | Всегда |
| `agent_id` | string | AGENT_ID из ENV | Если задан |
| `device.port` | string | Путь к устройству (`/dev/ttyUSB0`) | Всегда |
| `device.mode` | string | `serial` или `modem` | Всегда |
| `device.auto_detected` | bool | `true` если найден через auto-scan | Всегда |
| `device.usb_vid` | string | USB Vendor ID | Если определён |
| `device.usb_pid` | string | USB Product ID | Если определён |
| `device.manufacturer` | string | Производитель (u-blox, Quectel, …) | Если определён |
| `device.chip` | string | Модель чипа (NEO-M8, EC25, …) | Если определён |
| `device.has_vbat` | bool | Наличие battery backup | Если определён |
| `location.lat` | float64 | Широта (десятичные градусы) | При fix=true |
| `location.lon` | float64 | Долгота (десятичные градусы) | При fix=true |
| `location.alt` | float64 | Высота над уровнем моря, м | При fix=true |
| `gnss.fix` | bool | Фикс получен | Всегда |
| `gnss.provider` | string | `gps` / `glonass` / `galileo` / `beidou` / `mixed` / `unknown` | Всегда |
| `gnss.satellites` | int | Число использованных спутников | Всегда |
| `gnss.hdop` | float64 | Horizontal Dilution of Precision | При fix=true |
| `gnss.start_type` | string | `hot` / `warm` / `cold` / `unknown` | Всегда |
| `gnss.time_to_fix_ms` | int64 | Время от старта до фикса, мс | При fix=true |
| `time.utc` | string | UTC время фикса (ISO 8601) | При fix=true |
| `time.drift_ms` | float64 | Дрейф системного времени, мс | При fix=true |

### 7.2. Пример успешного результата (exit 0)

```json
{
  "probe": "gnss",
  "timestamp": 1710003600,
  "agent_hostname": "agent-nl-01",
  "agent_id": "nl-ams-01",
  "device": {
    "port": "/dev/ttyUSB0",
    "mode": "serial",
    "auto_detected": true,
    "usb_vid": "1546",
    "usb_pid": "01a8",
    "manufacturer": "u-blox",
    "chip": "NEO-M8",
    "has_vbat": true
  },
  "location": {
    "lat": 52.367600,
    "lon": 4.904100,
    "alt": 12.3
  },
  "gnss": {
    "provider": "mixed",
    "satellites": 9,
    "hdop": 0.8,
    "fix": true,
    "start_type": "hot",
    "time_to_fix_ms": 2340
  },
  "time": {
    "utc": "2024-03-09T22:00:01Z",
    "drift_ms": 12.4
  }
}
```

### 7.3. Пример при отсутствии фикса (exit 1)

```json
{
  "probe": "gnss",
  "timestamp": 1710003600,
  "agent_hostname": "agent-nl-01",
  "device": {
    "port": "/dev/ttyUSB0",
    "mode": "serial",
    "auto_detected": false,
    "manufacturer": "unknown"
  },
  "gnss": {
    "fix": false,
    "provider": "gps",
    "satellites": 2,
    "start_type": "cold",
    "time_to_fix_ms": 0
  }
}
```

### 7.4. Пример при ошибке устройства (exit 2)

```json
{
  "probe": "gnss",
  "timestamp": 1710003600,
  "agent_hostname": "agent-nl-01",
  "error": "serial open failed: no such file or directory",
  "error_code": 2
}
```

---

## 8. Логирование

### 8.1. stdout

Исключительно структурированный JSON. Одна строка на запуск. Никаких других выводов.

### 8.2. stderr

- `[INFO]` — подключение к устройству, результат auto-scan, итог запуска
- `[WARN]` — таймаут фикса, число спутников ниже `GPS_MIN_SATELLITES`, ошибки парсинга отдельных предложений
- `[ERROR]` — фатальные ошибки: устройство недоступно, ошибка конфигурации
- `[DEBUG]` — сырые NMEA-строки и состояние парсера (только при `GPS_DEBUG_NMEA=true`)

---

## 9. Nomad Job Definition

### 9.1. Стационарные точки — раз в час

```hcl
job "gnss-probe" {
  type      = "batch"
  namespace = "monitoring"

  periodic {
    crons            = ["0 * * * *"]   # ровно раз в час
    prohibit_overlap = true
    time_zone        = "UTC"
  }

  constraint {
    attribute = "${meta.has_gnss}"
    value     = "true"
  }

  group "probe" {
    restart { attempts = 0; mode = "fail" }

    task "gnss-probe" {
      driver = "docker"

      config {
        image   = "registry.yourorg.com/gnss-probe:latest"
        devices = [{ host_path = "/dev/ttyUSB0", container_path = "/dev/ttyUSB0" }]
        volumes = ["/var/lib/gnss-probe:/var/lib/gnss-probe"]
      }

      env {
        GPS_DEVICE             = "/dev/ttyUSB0"
        GPS_BAUDRATE           = "9600"
        GPS_READ_DURATION      = "5s"
        GPS_FIX_TIMEOUT_HOT    = "10s"
        GPS_FIX_TIMEOUT_WARM   = "45s"
        GPS_FIX_TIMEOUT_COLD   = "120s"
        GPS_MIN_SATELLITES     = "4"
        AGENT_ID               = "${node.unique.name}"
      }

      resources { cpu = 50; memory = 32 }
      kill_timeout = "130s"   # > GPS_FIX_TIMEOUT_COLD + буфер
    }
  }
}
```

### 9.2. Auto-scan вариант

```hcl
config {
  image      = "registry.yourorg.com/gnss-probe:latest"
  privileged = true
  volumes    = ["/dev:/dev", "/var/lib/gnss-probe:/var/lib/gnss-probe"]
}

env {
  GPS_DEVICE             = "auto"
  GPS_SCAN_TIMEOUT       = "3s"
  GPS_BAUDRATE           = "9600"
  GPS_READ_DURATION      = "5s"
  GPS_FIX_TIMEOUT_HOT    = "10s"
  GPS_FIX_TIMEOUT_WARM   = "45s"
  GPS_FIX_TIMEOUT_COLD   = "120s"
  GPS_ASSUME_COLD_START  = "false"
}

kill_timeout = "135s"   # scan_timeout * N_ports + fix_timeout_cold + буфер
```

---

## 10. Поддержка Quectel LTE-модемов (Modem Mode)

Узлы могут быть оснащены LTE-модемами Quectel EC25, EC200 или EM05 со встроенным GNSS-движком. Это принципиально иная архитектура по сравнению с простым serial GNSS-приёмником: GNSS-движок нужно активировать через AT-команды, а данные поступают через отдельный виртуальный USB-порт.

### 10.1. Поддерживаемые модели

| Модель | Форм-фактор | LTE Cat | GNSS системы | VID | PID | Baud |
|---|---|---|---|---|---|---|
| EC25 | Mini PCIe | Cat 4 | GPS+GLO+BDS+GAL | 2C7C | 0125 | 9600 |
| EC200A | LCC/PCIe | Cat 1 | GPS+GLO+BDS | 2C7C | 6005 | 115200 |
| EC200U | LCC/PCIe | Cat 1-bis | GPS+GLO+BDS | 2C7C | 6001 | 115200 |
| EC200T | LCC/PCIe | Cat 1 | GPS+GLO+BDS | 2C7C | 6002 | 115200 |
| EM05 | M.2 NGFF | Cat 4 | GPS+GLO+BDS+GAL | 2C7C | 030E | 115200 |

> **ℹ️ VID общий:** Все модемы Quectel используют один USB Vendor ID: `2C7C`. Проба определяет конкретную модель по Product ID и автоматически переключается в modem mode.

### 10.2. Архитектура виртуальных USB-портов

При подключении по USB каждый модем Quectel создаёт несколько виртуальных последовательных портов. Номер порта зависит от порядка устройств в системе, поэтому проба определяет нужные интерфейсы по `bInterfaceNumber` из sysfs, а не по жёстко прописанному `ttyUSBN`.

| IF# | Роль | Порт (типичный) | Назначение | Нужен пробе |
|---|---|---|---|---|
| 0 | Diagnostic / DM | ttyUSB0 | Диагностический интерфейс Qualcomm | — |
| **1** | **NMEA output** | **ttyUSB1** | **Поток NMEA-предложений от GNSS-движка** | **✅ Да** |
| **2** | **AT-команды** | **ttyUSB2** | **Управление: AT+QGPS=1 / AT+QGPSEND** | **✅ Да** |
| 3 | Modem / PPP | ttyUSB3 | Данные LTE, PPP-соединение | — |
| 4 | ADB (опционально) | ttyUSB4 | Только на некоторых прошивках | — |

```bash
# Определение портов через sysfs
# /sys/bus/usb/devices/<bus>-<port>/  →  VID=2C7C, PID=0125 (EC25)
# /sys/bus/usb/devices/<bus>-<port>:1.<IF>/tty/ttyUSB*
#
# bInterfaceNumber = 1  →  NMEA port  (/dev/ttyUSB1)
# bInterfaceNumber = 2  →  AT port    (/dev/ttyUSB2)
#
# Проба читает bInterfaceNumber для каждого ttyUSB* и маппит на роль.
```

### 10.3. Последовательность AT-команд

| AT-команда | Описание | Приоритет |
|---|---|---|
| `AT+QGPS=1` | Включить GNSS-движок | **Обязательно** |
| `AT+QGPS?` | Проверить статус: `+QGPS: 1` = работает | Диагностика |
| `AT+QGPSCFG="nmeasrc",1` | Включить вывод NMEA на USB-порту | **Обязательно** |
| `AT+QGPSCFG="gpsnmeatype",31` | Все GPS предложения: GGA+RMC+GSV+GSA+VTG | Рекомендуется |
| `AT+QGPSCFG="glonassnmeatype",1` | GLONASS NMEA предложения | Рекомендуется |
| `AT+QGPSCFG="galileonmeatype",1` | Galileo NMEA (EC25, EM05) | Рекомендуется |
| `AT+QGPSCFG="beidounmeatype",1` | BeiDou NMEA предложения | Рекомендуется |
| `AT+QGPSCFG="autogps",0` | Отключить автозапуск GPS при включении модема | Рекомендуется |
| `AT+QGPSCFG="gnssconfig",2` | GPS+GLONASS+BeiDou (требует reboot модема) | Рекомендуется |
| `AT+QGPSXTRA=1` | Включить XTRA assisted GPS | Опционально |
| `AT+QGPSXTRATIME="YYYY/MM/DD,..."` | Передать текущее UTC-время для XTRA | При XTRA |
| `AT+QGPSEND` | Выключить GNSS-движок по завершении сбора | **Обязательно** |

```bash
# Пример полной инициализации
AT+QGPS=1                          # включить GNSS
AT+QGPSCFG="nmeasrc",1             # NMEA на USB
AT+QGPSCFG="gpsnmeatype",31        # GGA+RMC+GSV+GSA+VTG
AT+QGPSCFG="glonassnmeatype",1     # GLONASS
AT+QGPSCFG="galileonmeatype",1     # Galileo (EC25, EM05)
AT+QGPSCFG="beidounmeatype",1      # BeiDou

# После инициализации — читаем NMEA с ttyUSB1 (обычный парсер)

# По завершении сбора
AT+QGPSEND                         # выключить GNSS
```

### 10.4. XTRA Assisted GPS

Quectel поддерживает проприетарный XTRA — загрузку файла эфемерид с серверов Qualcomm. Поскольку модемы EC25/EC200/EM05 обеспечивают LTE-подключение на самом узле, XTRA особенно эффективен.

| Сценарий | Cold start без XTRA | Cold start с XTRA |
|---|---|---|
| Время до фикса | 60–150 секунд | **2–8 секунд** |
| Требования | Только антенна с видом на небо | Интернет через LTE + файл (< 3 дней) |
| ENV | `GPS_ASSUME_COLD_START=true`, `GPS_FIX_TIMEOUT_COLD=120s` | `GPS_XTRA_ENABLE=true`, `GPS_FIX_TIMEOUT_HOT=10s` |

Серверы XTRA:
- `http://xtrapath1.izatcloud.net/xtra3grc.bin`
- `http://xtrapath2.izatcloud.net/xtra3grc.bin`
- `http://xtrapath3.izatcloud.net/xtra3grc.bin`

Для GPS+GLONASS использовать `xtra2.bin` вместо `xtra3grc.bin`.

### 10.5. Сравнение: serial mode vs modem mode

| Параметр | Serial режим | Modem режим |
|---|---|---|
| Тип устройства | Простой GNSS-приёмник | Quectel LTE-модем с GNSS |
| ENV | `GPS_MODE=serial` | `GPS_MODE=modem` |
| Портов для GPS | 1 (NMEA) | 2 (AT-команды + NMEA) |
| Активация GNSS | Не нужна (всегда активен) | `AT+QGPS=1` перед чтением |
| Завершение | Просто закрыть порт | `AT+QGPSEND`, затем закрыть |
| Определение портов | По VID/PID → один порт | По VID/PID → 2 порта по `bInterfaceNumber` |
| XTRA/AGPS | `$PSRF150` / UBX-AID / PMTK740 | `AT+QGPSXTRA` (проще и надёжнее) |
| Cold start (без XTRA) | 60–180s | 60–150s |
| Cold start (с XTRA) | 15–30s | **2–5s** (интернет через LTE) |
| Hot start | 1–5s (с VBAT) | 1–3s |

### 10.6. GPS_MODE и автовыбор режима

```
GPS_MODE=serial  →  Обычный serial GNSS-приёмник (текущее поведение)
GPS_MODE=modem   →  Quectel / другой AT-командный модем
GPS_MODE=auto    →  Автовыбор по VID:
                    VID 2C7C (Quectel)  → modem mode
                    VID 1546 / 0e8d     → serial mode
                    Остальные           → serial mode + NMEA probe
```

### 10.7. Новые ENV-переменные для modem mode

| Переменная | По умолчанию | Описание |
|---|---|---|
| `GPS_MODE` | `serial` | `serial` / `modem` / `auto` |
| `GPS_MODEM_AT_PORT` | — | Явный путь к AT-порту |
| `GPS_MODEM_NMEA_PORT` | — | Явный путь к NMEA-порту |
| `GPS_MODEM_BAUD` | `115200` | Скорость для AT и NMEA портов |
| `GPS_MODEM_INIT_TIMEOUT` | `5s` | Таймаут ответа на AT-команды |
| `GPS_XTRA_ENABLE` | `false` | Включить XTRA |
| `GPS_XTRA_TIME_SYNC` | `true` | Передавать время через AT+QGPSXTRATIME |

### 10.8. Nomad job для узлов с Quectel

```hcl
task "gnss-probe" {
  driver = "docker"

  config {
    image      = "registry.yourorg.com/gnss-probe:latest"
    privileged = true
    volumes    = ["/dev:/dev", "/var/lib/gnss-probe:/var/lib/gnss-probe"]
  }

  env {
    GPS_MODE               = "auto"         # автовыбор serial vs modem
    GPS_BAUDRATE           = "115200"        # Quectel использует 115200
    GPS_READ_DURATION      = "5s"
    GPS_FIX_TIMEOUT_HOT    = "10s"
    GPS_FIX_TIMEOUT_WARM   = "45s"
    GPS_FIX_TIMEOUT_COLD   = "120s"
    GPS_XTRA_ENABLE        = "true"          # XTRA через LTE
    GPS_XTRA_TIME_SYNC     = "true"
    GPS_MIN_SATELLITES     = "4"
    AGENT_ID               = "${node.unique.name}"
  }

  resources { cpu = 50; memory = 32 }
  kill_timeout = "135s"
}
```

---

## 11. Обработка ошибок в Modem Mode

### 11.1. Таймауты AT-команд

| Команда | Таймаут | Действие при таймауте |
|---|---|---|
| `AT+QGPS=1` | 5s | Retry 1 раз, затем exit 2 (DeviceError) |
| `AT+QGPSCFG=...` | 2s | Продолжить без этой настройки, WARNING |
| `AT+QGPSXTRA=1` | 10s | Отключить XTRA, продолжить без него |
| `AT+QGPSEND` | 3s | Игнорировать ошибку, продолжить shutdown |

### 11.2. Коды ошибок AT-команд

```
AT+QGPS=1
ERROR                    → GNSS уже запущен, игнорировать
+CME ERROR: 504          → GNSS не поддерживается, exit 2
+CME ERROR: 505          → Нет антенны, exit 2
+CME ERROR: 516          → Недостаточно памяти, exit 2

AT+QGPSXTRA=1
ERROR                    → XTRA не поддерживается, продолжить без XTRA
+CME ERROR: 549          → Файл XTRA устарел (>3 дней), продолжить без XTRA
```

### 11.3. Fallback при недоступности XTRA

Если `GPS_XTRA_ENABLE=true`, но XTRA недоступен:

1. Проба пытается `AT+QGPSXTRA=1`
2. При ошибке — WARNING в stderr: `XTRA unavailable, falling back to cold start`
3. Переключение таймаута: `GPS_FIX_TIMEOUT_HOT` → `GPS_FIX_TIMEOUT_COLD`
4. Продолжение работы в обычном режиме
5. В JSON: `"xtra_used": false`

### 11.4. Определение портов через bInterfaceNumber

```bash
# Алгоритм определения AT и NMEA портов
for device in /sys/bus/usb/devices/*; do
    vid=$(cat $device/idVendor 2>/dev/null)
    pid=$(cat $device/idProduct 2>/dev/null)
    
    if [[ "$vid" == "2c7c" ]]; then  # Quectel
        for interface in $device:1.*; do
            ifnum=$(cat $interface/bInterfaceNumber 2>/dev/null)
            tty=$(ls $interface/tty/ 2>/dev/null)
            
            case "$ifnum" in
                01) nmea_port="/dev/$tty" ;;  # NMEA output
                02) at_port="/dev/$tty" ;;    # AT commands
            esac
        done
    fi
done

# Если bInterfaceNumber не читается:
# - Попытка открыть все ttyUSB* последовательно
# - Отправка "AT\r\n" на каждый порт
# - Порт с ответом "OK" = AT-порт
# - Порт с NMEA-строками = NMEA-порт
```

---

## 12. Troubleshooting

### 12.1. Типичные проблемы

| Проблема | Симптом | Решение |
|---|---|---|
| Устройство не найдено | exit 2, "no such file" | Проверить `lsusb`, добавить `--device` в docker |
| Permission denied | exit 2, "permission denied" | Добавить `--privileged` или udev rules |
| Таймаут фикса | exit 1, satellites < 4 | Проверить антенну, увеличить таймаут |
| NMEA не приходит | exit 1, satellites = 0 | Проверить baudrate, для Quectel: AT+QGPSCFG |
| Quectel: ERROR на AT+QGPS | exit 2 | GNSS уже запущен другим процессом, AT+QGPSEND |
| XTRA не работает | WARNING в stderr | Проверить интернет, файл устарел (>3 дней) |
| Неверный provider | "gps" вместо "mixed" | Проверить GSV предложения, не GGA талкер |

### 12.2. Диагностические команды

```bash
# Проверка USB-устройств
lsusb | grep -E "1546|2c7c|0e8d"  # u-blox, Quectel, MediaTek

# Проверка serial портов
ls -la /dev/ttyUSB* /dev/ttyACM*

# Чтение VID/PID
for dev in /dev/ttyUSB*; do
    udevadm info -q property -n $dev | grep -E "ID_VENDOR_ID|ID_MODEL_ID"
done

# Ручное чтение NMEA (для serial режима)
stty -F /dev/ttyUSB0 9600 raw -echo
cat /dev/ttyUSB0

# Ручная проверка Quectel (modem режим)
screen /dev/ttyUSB2 115200  # AT-порт
AT+QGPS?
+QGPS: 1  # 1 = запущен, 0 = остановлен

AT+QGPSLOC=2  # Получить текущую позицию
+QGPSLOC: 123456.0,52.3676,4.9041,1.2,62.5,3,0.0,0.0,0.0,090324,09
```

### 12.3. Примеры stderr-логов

**Успешный запуск (serial mode, hot start):**
```
[INFO] gnss-probe v3.0 starting
[INFO] Device: /dev/ttyUSB0 (u-blox NEO-M8, VID=1546 PID=01a8)
[INFO] Mode: serial, baudrate: 9600
[INFO] Start type detected: hot (GNTXT)
[INFO] Fix timeout: 10s (hot start)
[INFO] Fix acquired in 2.3s, satellites: 9, HDOP: 0.8
[INFO] Provider: mixed (GPS+GLONASS)
[INFO] Reading NMEA for 5s...
[INFO] Result emitted, exit 0
```

**Успешный запуск (modem mode, XTRA):**
```
[INFO] gnss-probe v3.0 starting
[INFO] Device: auto-detected Quectel EC25 (VID=2c7c PID=0125)
[INFO] Mode: modem, AT port: /dev/ttyUSB2, NMEA port: /dev/ttyUSB1
[INFO] Sending AT+QGPS=1... OK
[INFO] XTRA enabled, syncing time... OK
[INFO] Start type detected: hot (XTRA assisted)
[INFO] Fix timeout: 10s (hot start)
[INFO] Fix acquired in 3.1s, satellites: 11, HDOP: 0.7
[INFO] Provider: mixed (GPS+GLONASS+BeiDou)
[INFO] Sending AT+QGPSEND... OK
[INFO] Result emitted, exit 0
```

**Таймаут без фикса:**
```
[INFO] gnss-probe v3.0 starting
[INFO] Device: /dev/ttyUSB0 (generic CH340, VID=1a86 PID=7523)
[INFO] Mode: serial, baudrate: 9600
[WARN] No VBAT detected, assuming cold start
[INFO] Fix timeout: 90s (cold start)
[WARN] 30s elapsed, satellites: 2, waiting...
[WARN] 60s elapsed, satellites: 3, waiting...
[WARN] 90s timeout reached, no fix acquired
[WARN] Partial result: satellites=3, provider=gps
[INFO] Result emitted (fix=false), exit 1
```

**Ошибка устройства:**
```
[INFO] gnss-probe v3.0 starting
[ERROR] Failed to open /dev/ttyUSB0: no such file or directory
[ERROR] Retry 1/3 in 1s...
[ERROR] Retry 2/3 in 1s...
[ERROR] Retry 3/3 in 1s...
[ERROR] Device unavailable after 3 attempts
[INFO] ErrorResult emitted, exit 2
```

---

## 13. Security Considerations

### 13.1. Доступ к устройствам

- Контейнер требует доступ к `/dev/ttyUSB*` — используйте `--device` вместо `--privileged` где возможно
- Для auto-scan необходим `--privileged` или монтирование `/dev` и `/sys`
- udev rules для ограничения доступа:

```bash
# /etc/udev/rules.d/99-gnss.rules
SUBSYSTEM=="tty", ATTRS{idVendor}=="1546", ATTRS{idProduct}=="01a8", GROUP="gnss", MODE="0660"
SUBSYSTEM=="tty", ATTRS{idVendor}=="2c7c", GROUP="gnss", MODE="0660"
```

### 13.2. XTRA и сетевой доступ

- XTRA серверы: `xtrapath[1-3].izatcloud.net` — Qualcomm CDN
- Не требует аутентификации
- Файл эфемерид обновляется каждые 3 дня
- Размер файла: ~50 КБ
- Рекомендация: кэшировать файл на хосте, монтировать в контейнер

### 13.3. Кэш позиции

- `/var/lib/gnss-probe/last_fix.json` содержит координаты последнего фикса
- Не содержит PII, но раскрывает местоположение агента
- Рекомендация: `chmod 600`, владелец = пользователь контейнера

### 13.4. Логирование

- stdout: только JSON, может содержать координаты
- stderr: диагностика, не содержит координат
- При `GPS_DEBUG_NMEA=true`: сырые NMEA в stderr — содержат координаты
- Рекомендация: фильтровать stdout перед отправкой в централизованное хранилище

---

## 14. Совместимость и требования

### 14.1. Операционные системы

| ОС | Версия | Статус | Примечание |
|---|---|---|---|
| Linux | Kernel 4.9+ | ✅ Полная поддержка | sysfs, udev |
| Linux | Kernel 3.x | ⚠️ Ограниченная | VID/PID может не читаться |
| Windows | — | ❌ Не поддерживается | COM-порты, другой sysfs |
| macOS | — | ❌ Не поддерживается | `/dev/cu.*`, другой sysfs |

### 14.2. Архитектуры CPU

- `amd64` (x86_64) — основная платформа
- `arm64` (aarch64) — Raspberry Pi, edge устройства
- `armv7` — старые ARM-платформы

Сборка: `GOARCH=arm64 GOARM=7 go build`

### 14.3. Docker

- Docker Engine 20.10+
- Поддержка `--device` флага
- Для auto-scan: `--privileged` или bind mount `/dev` + `/sys`

### 14.4. Зависимости

```go
// go.mod
module github.com/yourorg/gnss-probe

go 1.22

require (
    github.com/adrianmo/go-nmea v1.8.0
    go.bug.st/serial v1.6.2
)
```

---

## 15. Метрики производительности

### 15.1. Потребление ресурсов

| Режим | CPU (avg) | Memory (RSS) | Disk I/O |
|---|---|---|---|
| Serial mode, hot start | 5–10% | 8–12 МБ | Нет |
| Serial mode, cold start | 5–10% | 8–12 МБ | Нет |
| Modem mode, без XTRA | 10–15% | 10–15 МБ | Нет |
| Modem mode, с XTRA | 15–25% | 12–18 МБ | 50 КБ (загрузка файла) |

> Измерения на Intel Xeon E5-2680 v4, 1 vCPU, контейнер без лимитов

### 15.2. Время выполнения

| Сценарий | Время до фикса | Общее время пробы |
|---|---|---|
| u-blox NEO-M8, hot start | 2–5s | 7–10s |
| u-blox NEO-M8, cold start | 30–60s | 35–65s |
| CH340 generic, cold start | 60–120s | 65–125s |
| Quectel EC25, XTRA | 2–8s | 7–13s |
| Quectel EC25, без XTRA | 60–90s | 65–95s |

### 15.3. Размер образа

```dockerfile
# Multi-stage build
FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o gnss-probe .

FROM scratch
COPY --from=builder /build/gnss-probe /gnss-probe
ENTRYPOINT ["/gnss-probe"]
```

- Binary (stripped): ~4.8 МБ
- Docker image (scratch): ~5.2 МБ
- Docker image (alpine): ~12 МБ

---

## 16. Vector Configuration

### 16.1. Сбор JSON из stdout

```toml
# /etc/vector/vector.toml

[sources.nomad_gnss_stdout]
type = "docker_logs"
include_containers = ["gnss-probe"]

[transforms.gnss_parse]
type = "remap"
inputs = ["nomad_gnss_stdout"]
source = '''
  . = parse_json!(.message)
  .source_type = "gnss-probe"
  .timestamp = to_timestamp!(.timestamp)
'''

[transforms.gnss_filter_success]
type = "filter"
inputs = ["gnss_parse"]
condition = '.gnss.fix == true'

[sinks.gnss_to_elasticsearch]
type = "elasticsearch"
inputs = ["gnss_filter_success"]
endpoint = "https://es.yourorg.com"
index = "gnss-metrics-%Y.%m.%d"

[sinks.gnss_errors_to_loki]
type = "loki"
inputs = ["gnss_parse"]
endpoint = "https://loki.yourorg.com"
labels.job = "gnss-probe"
labels.agent = "{{ agent_hostname }}"
```

### 16.2. Алерты на основе exit codes

```yaml
# Prometheus Alertmanager
groups:
  - name: gnss-probe
    rules:
      - alert: GNSSNoFixRepeated
        expr: |
          count_over_time(
            nomad_allocation_exit_code{task="gnss-probe", exit_code="1"}[3h]
          ) >= 3
        annotations:
          summary: "GNSS probe failed to acquire fix 3 times in 3 hours"
          description: "Agent {{ $labels.node }} may have antenna issues"

      - alert: GNSSDeviceError
        expr: |
          nomad_allocation_exit_code{task="gnss-probe", exit_code="2"} == 1
        annotations:
          summary: "GNSS device unavailable"
          description: "Agent {{ $labels.node }} cannot access GNSS device"
```

---

## 17. Технические ограничения

- Язык: Go 1.22+
- Зависимости: `github.com/adrianmo/go-nmea` v1.8.0, `go.bug.st/serial` v1.6.2
- Binary: статически слинкован (`CGO_ENABLED=0`), ~4.8 МБ
- Docker: multi-stage build, scratch image, ~5.2 МБ
- Reconnect: до 3 попыток с задержкой 1s при открытии serial порта
- Partial result: всегда эмитируется JSON, даже при exit 1
- VID/PID: читается из Linux sysfs — не требует lsusb/udevadm
- Кэш позиции: `GPS_CACHE_PATH` (default `/var/lib/gnss-probe/last_fix.json`) — монтируется как volume
- ARM совместимость: `GOARCH=arm64` для Raspberry Pi / edge устройств
- Modem mode: два параллельных порта (AT + NMEA), определение через `bInterfaceNumber` из sysfs
- XTRA: загрузка файла эфемерид через LTE для Quectel, сокращение cold start до 2–8 секунд
- Максимальное время работы: `kill_timeout` в Nomad должен быть > max(GPS_FIX_TIMEOUT_*) + 10s
- Concurrency: один экземпляр на устройство, `prohibit_overlap = true` в Nomad periodic job

---

## 18. Структура проекта

```
gnss-probe/
├── main.go                      — точка входа, exit codes
├── go.mod / go.sum
├── Dockerfile                   — multi-stage build, scratch image
├── nomad/
│   ├── gnss-probe.hcl             — serial mode, явное устройство
│   ├── gnss-probe-autoscan.hcl    — serial mode, auto-scan
│   └── gnss-probe-quectel.hcl     — modem mode, Quectel + XTRA
└── internal/
    ├── config/config.go           — ENV переменные, валидация, TimeoutFor()
    ├── device/detect.go           — whitelist VID/PID, sysfs scan, NMEA probe
    ├── nmea/parser.go             — парсинг NMEA, provider через GSV, start_type
    ├── cache/cache.go             — чтение/запись last_fix.json (атомарно)
    ├── assist/assist.go           — assisted start: $PUBX,04 / $PMTK740 / $PSRF104
    ├── probe/
    │   ├── probe.go                 — диспетчер serial vs modem
    │   └── serial.go                — serial mode: open → assist → read NMEA → cache
    ├── modem/
    │   ├── modem.go                 — FindPorts (sysfs + fallback), AT()
    │   └── run.go                   — modem mode: AT init → XTRA → read NMEA → cache
    └── result/result.go           — JSON структуры, Emit(), константы exit codes
```

---

## Changelog

### v3.0 — Поддержка Quectel LTE-модемов

- Добавлен раздел 10: полное описание modem mode для EC25, EC200A/U/T, EM05
- Добавлены VID/PID Quectel (2C7C) в whitelist auto-detection с priority 95
- Добавлен `GPS_MODE=serial|modem|auto` с автовыбором по VID
- Описан алгоритм определения AT-порта и NMEA-порта через `bInterfaceNumber` sysfs
- Добавлена таблица AT-команд: QGPS, QGPSCFG, QGPSXTRA, QGPSEND
- Добавлен раздел XTRA Assisted GPS — сравнение cold start с/без XTRA
- Добавлены ENV: GPS_MODE, GPS_MODEM_AT_PORT, GPS_MODEM_NMEA_PORT, GPS_MODEM_BAUD, GPS_MODEM_INIT_TIMEOUT, GPS_XTRA_ENABLE, GPS_XTRA_TIME_SYNC
- Добавлено поле `device.mode` в выходной JSON
- Nomad job обновлён для Quectel: baud 115200, GPS_MODE=auto, XTRA
- **Раздел 5.1–5.4 ПЕРЕРАБОТАН:** задокументировано ключевое отличие EC25 от u-blox — GGA/RMC у EC25 всегда имеют талкер `GP` даже при активном GLONASS. Это задокументированное поведение согласно Quectel EC25 GNSS AT Commands Manual v1.1 раздел 1.2
- Добавлена сравнительная таблица талкеров u-blox vs EC25 для всех типов предложений
- Добавлены правила парсинга для EC25: `provider` определяется через GSV талкеры (`$GLGSV`/`$GAGSV`/`$PQGSV`), а не через талкер GGA/RMC
- Исправлена логика определения provider: убрана ошибочная проверка `talker(GGA)==GN`
- Добавлена ENV `GPS_CACHE_PATH` для переопределения пути к файлу кэша позиции
- Уточнена логика раздела 3.2 п.2: VBAT + кэш < 4ч → hot start; нет VBAT → cold start
- Добавлены разделы 11–18: обработка ошибок modem mode, troubleshooting, security, совместимость, метрики, Vector, структура проекта
- Исправлено: дублирующиеся строки в таблице 5.3 удалены
- Исправлено: порог priority в разделе 4.1 уточнён (> 80 вместо ≥ 90)

### v2.0 — Таймауты и USB Auto-Detection

- Добавлены явные exit codes (0 / 1 / 2 / 3)
- Добавлены три отдельных таймаута: GPS_FIX_TIMEOUT_HOT / WARM / COLD
- Добавлен детектор типа старта (hot / warm / cold) через GNTXT и прогрессию спутников
- Добавлено поле `gnss.start_type` и `gnss.time_to_fix_ms` в выходной JSON
- Добавлено поле `device.has_vbat` в выходной JSON
- Добавлен кэш последней позиции (`/var/lib/gnss-probe/last_fix.json`)
- Добавлен раздел USB Auto-Detection с whitelist VID/PID и алгоритмом sysfs
- Добавлен ENV `GPS_ASSUME_COLD_START` для принудительного cold-таймаута
- Исправлено: `drift_ms` → `float64` вместо `int`
- Исправлено: поле `provider` переименовано из `constellation`, теперь выводится из talker IDs
- Исправлено: эмиссия JSON при fatal error (ErrorResult с полем `error_code`)
- Номад: добавлен volume для кэша позиции; `kill_timeout` пересчитан
