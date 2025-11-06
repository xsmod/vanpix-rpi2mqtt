# rpi-stats2mqtt

Small agent that reads Raspberry Pi host statistics and publishes them to MQTT with Home Assistant auto-discovery.

Environment variables:
- MQTT_BROKER (default: tcp://localhost:1883)
- MQTT_USER
- MQTT_PASSWORD
- MQTT_CLIENT_ID (default: rpi-stats)
- MQTT_TOPIC_PREFIX (default: rpi-stats)
- INTERVAL_SECONDS (default: 30) — sampling/publish interval in seconds
- DEVICE_NAME (default: rpi)
- HA_DISCOVERY (default: true)
- HA_PREFIX (default: homeassistant)
- HOST_ROOT_PATH (default: "/") — path inside the container to use as the host root for disk/statfs reads (useful when mounting the host filesystem)

Build (for Raspberry Pi 5 / arm64):

```bash
# Build local binary
GOOS=linux GOARCH=arm64 go build -o rpi-stats2mqtt

# Build docker image (from project root)
docker build -t rpi-stats2mqtt:latest .
```

Run with docker:

```bash
docker run -d \
  --name rpi-stats \
  --restart unless-stopped \
  --net host \
  -e MQTT_BROKER="tcp://192.168.1.10:1883" \
  -e MQTT_USER="mqttuser" \
  -e MQTT_PASSWORD="mqttpass" \
  -e DEVICE_NAME="livingroom-pi" \
  -e INTERVAL_SECONDS="15" \
  rpi-stats2mqtt:latest
```

Docker-compose example (recommended for reporting host stats)

Notes:
- If you run the container without special mounts the agent will read the container's /proc and /sys and report container-level values.
- To report the host values you should either run in `network_mode: host` and mount the host filesystems into the container (recommended), or run the container with `--pid=host` and mounts for `/proc` and `/sys` (more intrusive).
- The example below mounts the host root read-only at `/host` and the host thermal sysfs to ensure correct temperature readings, and sets `HOST_ROOT_PATH=/host` so disk usage is measured on the host filesystem. All mounts are read-only for safety.

```yaml
version: '3.8'
services:
  rpi-stats:
    image: ghcr.io/xsmod/vanpix-rpistats2mqtt:latest
    container_name: rpi-stats
    network_mode: host          # allows IP/iface detection to see host interfaces
    environment:
      - MQTT_BROKER=tcp://192.168.1.10:1883
      - MQTT_USER=mqttuser
      - MQTT_PASSWORD=mqttpass
      - DEVICE_NAME=livingroom-pi
      - INTERVAL_SECONDS=15
      - HOST_ROOT_PATH=/host   # measure disk usage against the mounted host root
    volumes:
      - /:/host:ro             # mount host root read-only so disk/statfs reflect host
      - /sys/class/thermal:/sys/class/thermal:ro  # allow reading host thermal sensors
    restart: unless-stopped
```

Publishing via GoReleaser
- Tag a release and push: `git tag v1.0.0 && git push origin v1.0.0`
- GitHub Actions will build multi-arch images (amd64, arm64) and publish to GitHub Container Registry: `ghcr.io/xsmod/vanpix-rpistats2mqtt:{version}` and `:latest`.
- Pulling: `docker pull ghcr.io/xsmod/vanpix-rpistats2mqtt:latest`

Home Assistant will pick up sensors via MQTT discovery under the configured `HA_PREFIX` (default `homeassistant`).
