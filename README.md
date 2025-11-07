# vanpix-rpi2mqtt

Raspberry Pi host statistics -> MQTT + aggregated Home Assistant (HA) discovery.

Current behavior (v2+):
- One retained JSON payload with all metrics is published every interval at: `<MQTT_TOPIC_PREFIX>/state`.
- Availability (online/offline) is retained at: `<MQTT_TOPIC_PREFIX>/availability` (MQTT Last Will offline on crash).
- Optional HA discovery (single device document) published once at: `homeassistant/device/vanpix_rpi/config` when `HA_DISCOVERY=true`.
- HA entities read fields from the shared JSON using `value_template` (no per-sensor state topics).

Example state JSON:
```json
{
  "cpu_load": 3.1,
  "temperature_c": 46.875,
  "temperature_f": 116.38,
  "mem_total_mb": 8064,
  "mem_available_mb": 6393,
  "mem_free_mb": 1422,
  "disk_total_gb": 58.24,
  "disk_free_gb": 41.73,
  "uptime_days": 17.91,
  "ip": "192.168.68.250"
}
```

HA discovery document (abridged):
```json
{
  "device": {
    "ids": "<first configured HA identifier or vanpix_rpi>",
    "name": "VanPIX- RPI",
    "manufacturer": "github.com/xsmod",
    "model": "vanpix-rpi2mqtt",
    "sw": "v1.0.0"
  },
  "origin": { "name": "application" },
  "components": {
    "cpu_load": { "platform": "sensor", "state_topic": "vanpix_rpi/state", "value_template": "{{ value_json.cpu_load }}", "unit_of_measurement": "%" },
    "temperature_c": { "platform": "sensor", "state_topic": "vanpix_rpi/state", "value_template": "{{ value_json.temperature_c }}", "unit_of_measurement": "°C", "device_class": "temperature" }
  },
  "state_topic": "vanpix_rpi/state",
  "availability_topic": "vanpix_rpi/availability",
  "payload_available": "online",
  "payload_not_available": "offline",
  "qos": 2
}
```
Discovery topic path is fixed to the constant device ID: `homeassistant/device/vanpix_rpi/config`.

## Metrics
| Field | Description | Notes |
|-------|-------------|-------|
| cpu_load | CPU usage % over the last interval | First sample is 0.0 (baseline). Clamped 0–100. |
| temperature_c / temperature_f | CPU / SoC temperature (°C / °F) | Reads `/sys/class/thermal/thermal_zone0/temp`. Millidegrees auto-scaled. |
| mem_total_mb | Total RAM (from MemTotal) | From `/proc/meminfo` in MB. |
| mem_available_mb | Available RAM (MemAvailable or MemFree fallback) | Matches `available` column in `top`. |
| mem_free_mb | Free RAM (MemFree) | Raw free list. |
| disk_total_gb / disk_free_gb | Host filesystem size & free (GB) | Uses `statfs` on `HOST_ROOT_PATH` (default `/`). |
| uptime_days | Uptime in days with 2 decimals | Derived from `/proc/uptime`. |
| ip | Provided IP string | Only published if set via env or file. |

## Environment Variables
| Name | Default | Purpose |
|------|---------|---------|
| MQTT_BROKER | tcp://localhost:1883 | MQTT broker URI (supports tcp://host:port). |
| MQTT_USER | (empty) | MQTT username (omit for anonymous). |
| MQTT_PASSWORD | (empty) | MQTT password (ignored if user empty). |
| MQTT_CLIENT_ID | vanpix_rpi | MQTT client ID. |
| MQTT_TOPIC_PREFIX | vanpix_rpi | Prefix for state & availability topics. |
| INTERVAL_SECONDS | 30 | Sampling/publish interval (seconds). Must be >0. |
| HA_DISCOVERY | true | Set false to disable publishing the HA discovery document. |
| HA_PREFIX | homeassistant | Base HA discovery prefix. |
| HA_DEVICE_IDENTIFIERS | (empty) | Comma-separated identifiers; first used as `device.ids`. |
| HA_DEVICE_IDENTIFIER | (empty) | Fallback single identifier (if plural not set). |
| HOST_ROOT_PATH | / | Path for disk stats (mount host root read-only for host numbers). |
| APP_VERSION | (empty) | Explicit override for software version in discovery. |
| IP_ADDRESS | (empty) | Static IP string injected into state JSON. |
| IP_FILE | (empty) | Path to file containing IP string (first line trimmed). |

Removed / Ignored legacy vars: `DEVICE_NAME` (device name is fixed to "VanPIX- RPI"), any per-sensor topic overrides.

## Versioning
At build time you can inject version metadata (used as `sw` in discovery):
```bash
go build -ldflags "-X main.BuildTag=v1.3.0 -X main.BuildCommit=$(git rev-parse --short=7 HEAD)"
```
Resolution order: `APP_VERSION` env > `BuildTag` > short `BuildCommit` > `dev`.

## Building
### Local (arm64 Raspberry Pi)
```bash
GOOS=linux GOARCH=arm64 go build -o vanpix-rpi2mqtt .
```
### Docker
```bash
docker build -t ghcr.io/xsmod/vanpix-rpi2mqtt:dev .
```

## Running (basic)
```bash
docker run -d --name vanpix-rpi --restart unless-stopped \
  -e MQTT_BROKER=tcp://192.168.68.250:1883 \
  -e INTERVAL_SECONDS=15 \
  -e HA_DISCOVERY=true \
  -e HA_DEVICE_IDENTIFIER=vanpix_rpi_host \
  -e IP_ADDRESS=192.168.68.250 \
  ghcr.io/xsmod/vanpix-rpi2mqtt:dev
```

## Host vs Container Stats
If you run unmodified, memory/cpu/uptime reflect the container's namespaces. To approximate host stats:
- Run with `--pid=host` and/or mount `/proc` and `/sys` read-only (advanced) OR deploy on the host with minimal isolation.
- For disk stats, mount the host root read-only and set `HOST_ROOT_PATH=/host`.

Example (disk & thermal only):
```yaml
services:
  vanpix-rpi:
    image: ghcr.io/xsmod/vanpix-rpi2mqtt:latest
    container_name: vanpix-rpi
    environment:
      - MQTT_BROKER=tcp://192.168.68.250:1883
      - HA_DEVICE_IDENTIFIER=vanpix_rpi_host
      - HOST_ROOT_PATH=/host
    volumes:
      - /:/host:ro
      - /sys/class/thermal:/sys/class/thermal:ro
    restart: unless-stopped
```

## Home Assistant Integration
1. Ensure MQTT integration is configured in HA.
2. Set `HA_DISCOVERY=true` (default) and start the container.
3. HA will create entities grouped under the device defined in the discovery document.
4. Each entity uses `value_template` to extract its field from the single JSON state topic.
5. Availability handled automatically via online/offline retained messages.

## Logs
- Successful publish logs: `published <topic>`
- Connection retries are silent until final failure.
- On graceful shutdown: publishes offline then disconnects.

## Extending / Customizing
PRs welcome for:
- Additional sensors (e.g., load averages, GPU temp, voltage).
- Alternate discovery formats behind a toggle.
- Optional compression of JSON payload.

## License
MIT
