# vanpix-rpi2mqtt

Raspberry Pi host statistics -> MQTT + aggregated Home Assistant (HA) discovery.

Current behavior (v2+):
- One retained JSON payload with all metrics is published every interval at: `<MQTT_TOPIC_PREFIX>/state`.
- Availability (online/offline) is retained at: `<MQTT_TOPIC_PREFIX>/availability` (MQTT Last Will offline on crash).
- Optional HA discovery (single device document) published on every successful (re)connect at: `homeassistant/device/vanpix_rpi/config` when `HA_DISCOVERY=true` (retained, so HA always gets latest).
- HA entities read fields from the shared JSON using `value_template` (no per-sensor state topics).

Prefix normalization:
- If you set `MQTT_TOPIC_PREFIX` to a single segment like `vanpi`, it will be expanded to `vanpi/sensor/vanpix_rpi` automatically.
- If your prefix already has a slash (e.g., `vanpi/sensor/vanpix_rpi` or `custom/segment`), it is used as-is (trailing slash trimmed).

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
  "uptime_days": 17.91
}
```

HA discovery document (abridged):
```json
{
  "device": {
    "ids": "<first configured HA identifier or vanpix_rpi>"
  },
  "origin": { "name": "vanpix-rpi2mqtt" },
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

## Environment Variables

Each environment variable below includes whether it's optional and the default value; all are read from the environment at startup.

| Variable | Required | Default | Description |
|---|---:|---|---|
| MQTT_BROKER | optional | `tcp://localhost:1883` | MQTT broker URL to connect to (e.g. `tcp://192.168.1.10:1883`). |
| MQTT_USER | optional | `` (empty) | Username for broker authentication (leave empty for anonymous). |
| MQTT_PASSWORD | optional | `` (empty) | Password for broker authentication (used only if MQTT_USER is set). |
| MQTT_CLIENT_ID | optional | `vanpix_rpi` | MQTT client id used when connecting to the broker. |
| MQTT_TOPIC_PREFIX | optional | `vanpix_rpi` | Base topic prefix for published state and availability. If a single segment is supplied (e.g. `vanpi`) it is normalized to `<prefix>/sensor/<device_id>`. |
| INTERVAL_SECONDS | optional | `30` | How often (seconds) the aggregated JSON state is published. |
| HA_DISCOVERY | optional | `true` | Toggle Home Assistant MQTT discovery; set to `false` to disable discovery messages. |
| HA_PREFIX | optional | `homeassistant` | MQTT discovery prefix used by Home Assistant. |
| HA_DEVICE_IDENTIFIERS | optional | `` (none) | Comma-separated list of stable device identifiers (e.g. `serial123,mac:AA:BB:CC`). These will be published as `device.identifiers` in discovery payloads; values are trimmed and deduplicated. The configured `DEVICE_ID` is always the first identifier. |
| DEVICE_ID | optional | `vanpix_rpi` | Canonical device id used in discovery topics and as the first element of `device.identifiers`. Set `DEVICE_ID` to override the default. |
| HOST_ROOT_PATH | optional | `/` | When running in Docker you can mount the host root and set this to e.g. `/host` so disk stats reflect the host filesystem. |

Example — multiple identifiers:
```bash
export HA_DEVICE_IDENTIFIERS="serial123,mac:AA:BB:CC"
```

Example — resulting `device.identifiers` JSON (two cases):

When `HA_DEVICE_IDENTIFIERS` is set (and `DEVICE_ID=vanpix_rpi`):
```json
{
  "device": {
    "identifiers": ["vanpix_rpi", "serial123", "mac:AA:BB:CC", "dev-ident"]
  }
}
```

When `HA_DEVICE_IDENTIFIERS` is empty (no extra identifiers) the default device id is still included and friendly metadata is added:
```json
{
  "device": {
    "identifiers": ["vanpix_rpi"],
    "name": "VanPIX RPI",
    "manufacturer": "github.com/xsmod",
    "model": "vanpix-rpi2mqtt"
  }
}
```

Removed legacy: DEVICE_NAME, manufacturer overrides, temperature_f.

## Build
- Go: 1.25
- Dockerfile uses a multi-stage build for producing a compact runtime image.

### Local build
```bash
GOOS=linux GOARCH=arm64 go build -o vanpix-rpi2mqtt .
```

Note: the project no longer embeds a runtime version string by default. The binary is built directly with `go build` (or via the provided Dockerfile/.goreleaser). If you want to embed a version into the binary, re-introduce build-time ldflags and corresponding variables in `main.go` (for example `var BuildTag string`) and build with:
```bash
go build -ldflags "-X main.BuildTag=v1.2.3 -X main.BuildCommit=abcdef" -o vanpix-rpi2mqtt .
```

### Docker
```bash
docker build -t ghcr.io/xsmod/vanpix-rpi2mqtt:dev .
```

Troubleshooting Docker builds:
- If you target a specific architecture, pass build args so the builder cross-compiles accordingly:
```bash
docker build \
  --build-arg TARGETOS=linux \
  --build-arg TARGETARCH=amd64 \
  -t ghcr.io/xsmod/vanpix-rpi2mqtt:dev .
```
- To see detailed progress output:
```bash
docker build --progress=plain .
```

## Run
Minimum to test anonymous MQTT and HA discovery:
```bash
docker run --rm \
  -e MQTT_BROKER=tcp://192.168.68.250:1883 \
  -e INTERVAL_SECONDS=15 \
  -e HA_DISCOVERY=true \
  -e HA_DEVICE_IDENTIFIERS=vanpix_rpi \
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
      - HA_DEVICE_IDENTIFIERS=vanpix_rpi
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

## Discovery
- Discovery is republished on each successful (re)connect, but debounced (min 30s between publishes) to avoid flapping storms. The payload is retained.
