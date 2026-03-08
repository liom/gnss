# gnss-probe

Короткоживущий контейнер для сбора данных GNSS-приёмника. Запускается по расписанию через HashiCorp Nomad, пишет один JSON-результат в stdout и завершается.

## Возможности

- Поддержка **serial mode** (u-blox NEO, MediaTek, generic USB-UART)
- Поддержка **modem mode** (Quectel EC25, EC200A/U/T, EM05 через AT+QGPS)
- USB auto-detection по VID/PID через Linux sysfs
- Определение типа старта: hot / warm / cold (через `$GNTXT`/`$GPTXT` и прогрессию спутников)
- Кэш последней позиции для assisted warm start
- XTRA assisted GPS для Quectel (cold start → 2–8 сек)
- Корректное определение multi-constellation через GSV-талкеры (совместимо с Quectel EC25)
- Структурированный JSON в stdout, диагностика в stderr
- Статический бинарь, scratch Docker image (~5 МБ)

## Быстрый старт

```bash
# Serial mode, явное устройство
GPS_DEVICE=/dev/ttyUSB0 GPS_BAUDRATE=9600 GPS_READ_DURATION=5s GPS_FIX_TIMEOUT=60s ./gnss-probe

# Auto-scan
GPS_DEVICE=auto GPS_FIX_TIMEOUT_HOT=10s GPS_FIX_TIMEOUT_COLD=90s ./gnss-probe

# Quectel modem mode
GPS_MODE=auto GPS_BAUDRATE=115200 GPS_XTRA_ENABLE=true GPS_FIX_TIMEOUT_HOT=10s ./gnss-probe
```

## Exit Codes

| Код | Условие | JSON в stdout |
|-----|---------|---------------|
| 0 | Фикс получен | Полный результат |
| 1 | Таймаут без фикса | Частичный (`fix: false`) |
| 2 | Serial порт недоступен | ErrorResult |
| 3 | Неверные ENV переменные | ErrorResult |

## Конфигурация (ENV)

### Основные

| Переменная | По умолчанию | Описание |
|---|---|---|
| `GPS_DEVICE` | `auto` | Путь к устройству или `auto` для сканирования |
| `GPS_BAUDRATE` | `9600` | Скорость порта (115200 для Quectel) |
| `GPS_MODE` | `auto` | `serial` / `modem` / `auto` |
| `GPS_READ_DURATION` | `5s` | Время сбора NMEA после фикса |
| `GPS_FIX_TIMEOUT` | `60s` | Общий таймаут фикса (fallback) |
| `GPS_FIX_TIMEOUT_HOT` | — | Таймаут для hot start |
| `GPS_FIX_TIMEOUT_WARM` | — | Таймаут для warm start |
| `GPS_FIX_TIMEOUT_COLD` | — | Таймаут для cold start |
| `GPS_ASSUME_COLD_START` | `false` | Принудительный cold-таймаут |
| `GPS_MIN_SATELLITES` | `4` | Минимум спутников для валидного фикса |
| `AGENT_ID` | — | Идентификатор агента в JSON |

### Auto-scan

| Переменная | По умолчанию | Описание |
|---|---|---|
| `GPS_AUTO_SCAN` | `false` | Принудительный скан поверх GPS_DEVICE |
| `GPS_SCAN_TIMEOUT` | `3s` | Таймаут NMEA-пробы одного кандидата |

### Кэш позиции

| Переменная | По умолчанию | Описание |
|---|---|---|
| `GPS_CACHE_PATH` | `/var/lib/gnss-probe/last_fix.json` | Путь к файлу кэша |

### Modem mode (Quectel)

| Переменная | По умолчанию | Описание |
|---|---|---|
| `GPS_MODEM_AT_PORT` | — | Явный путь к AT-порту |
| `GPS_MODEM_NMEA_PORT` | — | Явный путь к NMEA-порту |
| `GPS_MODEM_BAUD` | `115200` | Скорость для AT и NMEA портов |
| `GPS_MODEM_INIT_TIMEOUT` | `5s` | Таймаут AT-команд инициализации |
| `GPS_XTRA_ENABLE` | `false` | Включить XTRA assisted GPS |
| `GPS_XTRA_TIME_SYNC` | `true` | Передавать время через AT+QGPSXTRATIME |

### Отладка

| Переменная | По умолчанию | Описание |
|---|---|---|
| `GPS_DEBUG_NMEA` | `false` | Вывод сырых NMEA-предложений в stderr |

## Выходной JSON

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
  "location": { "lat": 52.3676, "lon": 4.9041, "alt": 12.3 },
  "gnss": {
    "fix": true,
    "provider": "mixed",
    "satellites": 9,
    "hdop": 0.8,
    "start_type": "hot",
    "time_to_fix_ms": 2340
  },
  "time": { "utc": "2024-03-09T22:00:01Z", "drift_ms": 12.4 }
}
```

## Поддерживаемые устройства

| Производитель | Чип | VID | PID | Режим |
|---|---|---|---|---|
| u-blox | NEO-6M | 1546 | 01a7 | serial |
| u-blox | NEO-M8 | 1546 | 01a8 | serial |
| u-blox | NEO-9 | 1546 | 01a9 | serial |
| MediaTek | MT3329/MT3339 | 0e8d | 3329 | serial |
| Quectel | EC25 | 2c7c | 0125 | modem |
| Quectel | EC200A | 2c7c | 6005 | modem |
| Quectel | EC200U | 2c7c | 6001 | modem |
| Quectel | EC200T | 2c7c | 6002 | modem |
| Quectel | EM05 | 2c7c | 030e | modem |
| Silicon Labs | CP210x | 10c4 | ea60 | serial + NMEA probe |
| FTDI | FT232R | 0403 | 6001 | serial + NMEA probe |
| Prolific | PL2303 | 067b | 2303 | serial + NMEA probe |
| QinHeng | CH340/CH343 | 1a86 | 7523 | serial + NMEA probe |

## Сборка

```bash
# Локальная сборка
CGO_ENABLED=0 go build -ldflags="-s -w" -o gnss-probe .

# Docker (multi-stage, scratch image)
docker build -t gnss-probe:latest .

# Cross-compile для ARM
GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o gnss-probe .
```

## Структура проекта

```
gnss-probe/
├── main.go                      — точка входа, exit codes
├── internal/
│   ├── config/config.go         — ENV переменные, валидация, TimeoutFor()
│   ├── device/detect.go         — whitelist VID/PID, sysfs scan, NMEA probe
│   ├── nmea/parser.go           — парсинг NMEA, provider через GSV, start_type
│   ├── cache/cache.go           — чтение/запись last_fix.json (атомарно)
│   ├── assist/assist.go         — assisted start: $PUBX,04 / $PMTK740 / $PSRF104
│   ├── probe/
│   │   ├── probe.go             — диспетчер serial vs modem
│   │   └── serial.go            — serial mode: open → assist → read NMEA → cache
│   ├── modem/
│   │   ├── modem.go             — FindPorts (sysfs + fallback), AT()
│   │   └── run.go               — modem mode: AT init → XTRA → read NMEA → cache
│   └── result/result.go         — JSON структуры, Emit(), константы exit codes
```

## Кэш позиции

При успешном фиксе сохраняется в `GPS_CACHE_PATH` (по умолчанию `/var/lib/gnss-probe/last_fix.json`):

```json
{ "lat": 52.3676, "lon": 4.9041, "alt": 12.3, "timestamp": 1710003600, "hdop": 0.8 }
```

При следующем запуске:
- u-blox NEO: отправляет `$PUBX,04` (time sync)
- MediaTek MT33xx: отправляет `$PMTK740` (позиция) + `$PMTK741` (время)
- Generic: отправляет `$PSRF104` (SiRF position init)
- Если `has_vbat=true` и кэш < 4 часов → `start_type=hot`

Файл монтируется как volume в Nomad job: `/var/lib/gnss-probe:/var/lib/gnss-probe`

## Nomad

```hcl
job "gnss-probe" {
  type      = "batch"
  namespace = "monitoring"

  periodic {
    crons            = ["0 * * * *"]
    prohibit_overlap = true
    time_zone        = "UTC"
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
        GPS_DEVICE           = "/dev/ttyUSB0"
        GPS_BAUDRATE         = "9600"
        GPS_READ_DURATION    = "5s"
        GPS_FIX_TIMEOUT_HOT  = "10s"
        GPS_FIX_TIMEOUT_WARM = "45s"
        GPS_FIX_TIMEOUT_COLD = "120s"
        AGENT_ID             = "${node.unique.name}"
      }
      resources    { cpu = 50; memory = 32 }
      kill_timeout = "130s"
    }
  }
}
```

Полные примеры job definition (auto-scan, Quectel) — в `nomad/`.

## Зависимости

- `github.com/adrianmo/go-nmea` v1.8.0 — парсинг NMEA
- `go.bug.st/serial` v1.6.2 — serial I/O

## Требования

- Linux, kernel 4.9+ (sysfs VID/PID)
- Go 1.22+
- Docker 20.10+ (для контейнерного запуска)
