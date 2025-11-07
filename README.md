# vanpix-rpi2mqtt

Raspberry Pi host statistics -> MQTT + aggregated Home Assistant (HA) discovery.

Current behavior (v2+):
- One retained JSON payload with all metrics is published every interval at: `<MQTT_TOPIC_PREFIX>/state`.
- Availability (online/offline) is retained at: `<MQTT_TOPIC_PREFIX>/availability` (MQTT Last Will offline on crash).
- Optional HA discovery (single device document) published on every successful (re)connect at: `homeassistant/device/vanpix_rpi/config` when `HA_DISCOVERY=true` (retained, so HA always gets latest).
- HA entities read fields from the shared JSON using `value_template` (no per-sensor state topics).

Example state JSON:
```json
{
  "cpu_load": 3.14,
  "temperature": 46.9,
  "mem_total_mb": 8064,
  "mem_available_mb": 6393,
  "mem_free_mb": 1422,
  "disk_total_gb": 58,
  "disk_free_gb": 41,
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
    "cpu_load": { "platform": "sensor", "state_topic": "vanpix_rpi/state", "value_template": "{{ value_json.cpu_load }}", "unit_of_measurement": "%", "state_class": "measurement", "entity_category": "diagnostic" },
    "temperature": { "platform": "sensor", "state_topic": "vanpix_rpi/state", "value_template": "{{ value_json.temperature }}", "device_class": "temperature", "state_class": "measurement", "entity_category": "diagnostic" }
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
- cpu_load: CPU usage % over the last interval (2 decimals). First sample 0.0. Clamped 0–100.
- temperature: SoC temperature in °C.
- mem_total_mb / mem_available_mb / mem_free_mb: Memory stats in MB.
- disk_total_gb / disk_free_gb: Whole GB values (floored) of host filesystem.
- uptime_days: Uptime in days (2 decimals).
- ip: Provided IP string (env/file).

## Environment Variables
- MQTT_BROKER (default `tcp://localhost:1883`)
- MQTT_USER, MQTT_PASSWORD (optional; omit for anonymous)
- MQTT_CLIENT_ID (default `vanpix_rpi`)
- MQTT_TOPIC_PREFIX (default `vanpix_rpi`)
- INTERVAL_SECONDS (default 30)
- HA_DISCOVERY (default true)
- HA_PREFIX (default `homeassistant`)
- HA_DEVICE_IDENTIFIERS / HA_DEVICE_IDENTIFIER
- HOST_ROOT_PATH (default `/`)
- APP_VERSION
- IP_ADDRESS / IP_FILE

Removed legacy: DEVICE_NAME, manufacturer overrides, temperature_f.

## Build
- Go: 1.25
- Dockerfile uses multi-stage build and injects VERSION and VCS_REF.

### Local build
```bash
GOOS=linux GOARCH=arm64 go build -o vanpix-rpi2mqtt .
```

### Docker
```bash
docker build -t ghcr.io/xsmod/vanpix-rpi2mqtt:dev .
```

## Run
Minimum to test anonymous MQTT and HA discovery:
```bash
docker run --rm \
  -e MQTT_BROKER=tcp://192.168.68.250:1883 \
  -e INTERVAL_SECONDS=15 \
  -e HA_DISCOVERY=true \
  -e HA_DEVICE_IDENTIFIER=vanpix_rpi_host \
  ghcr.io/xsmod/vanpix-rpi2mqtt:dev
```

Host vs Container stats note:
- To reflect host disk, mount host root read-only and set HOST_ROOT_PATH=/host
- For temperature, mount `/sys/class/thermal` read-only. CPU/mem/uptime still reflect container namespaces unless you relax isolation further (e.g., `--pid=host`).

Docker Compose example:
```yaml
services:
  vanpix-rpi:
    image: ghcr.io/xsmod/vanpix-rpi2mqtt:latest
    environment:
      - MQTT_BROKER=tcp://192.168.68.250:1883
      - HA_DEVICE_IDENTIFIER=vanpix_rpi_host
      - HOST_ROOT_PATH=/host
    volumes:
      - /:/host:ro
      - /sys/class/thermal:/sys/class/thermal:ro
    restart: unless-stopped
```

## CI
- GitHub Actions release workflow uses GoReleaser to push per-arch images and create manifests for tagged releases.
- Commit workflow builds and pushes a multi-arch image tag `commit-<shortsha>` in a single step.

## Logs
- Connection retries are silent and only report final failure after attempts.
- On publish success: `published <topic>` including the topic.
- On graceful shutdown: publishes `offline` then disconnects.
