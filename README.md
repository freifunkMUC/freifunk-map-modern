# Freifunk Map Modern

A fast, modern web map for [Freifunk](https://freifunk.net/) mesh networks. Built as a single Go binary with zero external dependencies — embeds all web assets and serves everything from one process.

![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/License-AGPL--3.0-blue)
![Zero Dependencies](https://img.shields.io/badge/Dependencies-0-brightgreen)

## Features

- **Single binary** — zero runtime dependencies, embeds all frontend assets
- **Federation mode** — auto-discovers all Freifunk communities from api.freifunk.net
- **Real-time updates** via Server-Sent Events (SSE)
- **Grafana integration** — auto-discovers Grafana dashboards and renders per-node charts
- **Fast startup** — caches federation state for instant restarts
- **Responsive** dark-themed UI with Leaflet maps
- **Node details** with firmware, uptime, traffic charts, neighbour mesh view
- **Community filtering** with metacommunity grouping
- **Search** by hostname, node ID, or model
- **Device deprecation warnings** for end-of-life hardware
- **No external CDN** — all vendor assets (Leaflet, uPlot) are bundled
- **Privacy-friendly** — the only external request is to tile servers for the map background (FFMUC tiles by default, OpenStreetMap as fallback)

## Quick Start

```bash
# Clone and build
git clone https://github.com/freifunkMUC/freifunk-map-modern.git
cd freifunk-map-modern
make build

# Single community mode (e.g. FFMUC)
cp config.example.json config.json
# Edit config.json with your community's data URL
./freifunk-map config.json

# Federation mode (all German Freifunk communities)
./freifunk-map config.federation.json
```

Open http://localhost:8080

## Docker

```bash
docker build -t freifunk-map .
docker run -p 8080:8080 freifunk-map

# With custom config
docker run -p 8080:8080 -v ./config.json:/config.json freifunk-map
```

## Configuration

Copy `config.example.json` and adjust for your community:

```jsonc
{
  "listen": ":8080",
  "siteName": "My Freifunk Map",
  "dataURL": "https://map.example.net/data/meshviewer.json",
  "refreshInterval": "60s",

  // Optional: Grafana integration
  "grafanaURL": "https://stats.example.net",
  "grafanaDashboard": "/d/abc123/node?var-nodeid={NODE_ID}",

  // Map defaults
  "mapCenter": [52.52, 13.405],
  "mapZoom": 10,

  // Tile layers (at least one required)
  "tileLayers": [
    {
      "name": "FFMUC Tiles",
      "url": "https://tiles.ext.ffmuc.net/osm/{z}/{x}/{y}.png",
      "attribution": "&copy; OpenStreetMap contributors",
      "maxZoom": 19
    }
  ],

  // Optional: domain name mapping
  "domainNames": {
    "my_domain_01": "Domain North",
    "my_domain_02": "Domain South"
  },

  // Optional: header links
  "links": [
    { "title": "Website", "href": "https://example.freifunk.net" }
  ],

  // Optional: device picture URL template
  "devicePictureURL": "https://map.aachen.freifunk.net/pictures-svg/{MODEL}.svg",

  // Optional: link for EOL device warnings
  "eolInfoURL": "https://example.freifunk.net/router-erneuern"
}
```

### Federation Mode

To show all Freifunk communities on a single map, use federation mode:

```json
{
  "federation": true,
  "refreshInterval": "120s"
}
```

This auto-discovers communities from the [Freifunk API](https://api.freifunk.net/), probes their data sources, discovers Grafana dashboards, and merges all node data into a unified map. Discovery state is cached to disk for instant restarts.

See `config.federation.json` for a ready-to-use federation config.

## Configuration Reference

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `listen` | string | `":8080"` | HTTP listen address |
| `siteName` | string | `"Freifunk Map"` | Site title |
| `dataURL` | string | *required** | meshviewer.json URL |
| `refreshInterval` | string | `"60s"` | Data refresh interval |
| `federation` | bool | `false` | Enable federation mode |
| `grafanaURL` | string | | Grafana base URL for charts |
| `grafanaDashboard` | string | | Dashboard URL template with `{NODE_ID}` |
| `mapCenter` | [lat, lng] | `[48.13, 11.58]` | Default map center |
| `mapZoom` | int | `10` | Default zoom level |
| `tileLayers` | array | | Map tile layer definitions |
| `domainNames` | object | | Domain key → display name |
| `links` | array | | Header navigation links |
| `devicePictureURL` | string | | Device image URL template with `{MODEL}` |
| `eolInfoURL` | string | | Link for end-of-life device warnings |

*\* Not required when `federation: true`*

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /api/nodes` | All nodes (JSON array) |
| `GET /api/nodes/{id}` | Single node with neighbour details |
| `GET /api/links` | All mesh links |
| `GET /api/stats` | Aggregate statistics |
| `GET /api/config` | Client configuration (public, no secrets) |
| `GET /api/events` | SSE stream for real-time updates |
| `GET /api/communities` | Discovered communities (federation mode) |
| `GET /api/metrics/{id}` | Grafana time-series data for a node |

## Data Source Compatibility

The map supports these data formats:

- **meshviewer.json** — the standard Gluon/BATMAN meshviewer format (preferred)
- **nodelist.json** — simpler format used by some communities as fallback

In federation mode, it also handles communities with:
- Non-standard technical types (`ffmap`, `hopglass`)
- Nodelist endpoints without `.json` extension
- Data URLs at non-standard paths (discovered via meshviewer `config.json`)

## Project Structure

```
.
├── main.go                          # Entrypoint + web embed
├── internal/
│   ├── config/config.go             # Configuration types + loading
│   ├── store/store.go               # Node store, snapshot, diff engine
│   ├── sse/sse.go                   # Server-Sent Events hub
│   ├── federation/
│   │   ├── discover.go              # Community discovery + nodelist parsing
│   │   ├── grafana.go               # Grafana auto-discovery + cache
│   │   └── store.go                 # Federation store + state persistence
│   └── api/handlers.go              # HTTP API handlers + gzip middleware
├── web/
│   ├── index.html                   # Single-page app shell
│   ├── app.js                       # Frontend application
│   ├── app.css                      # Styles
│   └── vendor/                      # Bundled Leaflet, uPlot, MarkerCluster
├── config.example.json              # Single-community example config
├── config.federation.json           # Federation mode config
├── Dockerfile                       # Multi-stage Docker build
├── Makefile                         # Build targets
└── go.mod                           # Go module (zero dependencies)
```

## Building

```bash
make build          # Build for current platform
make dev            # Run with go run
make release        # Cross-compile for linux/amd64, linux/arm64, darwin/arm64
make docker         # Build Docker image
```

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Ensure `go build ./...` passes
5. Submit a pull request

## License

This project is licensed under the [GNU Affero General Public License v3.0](LICENSE).
