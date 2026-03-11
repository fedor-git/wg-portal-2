# wg-portal-2

[![Build Status](https://github.com/fedor-git/wg-portal-2/actions/workflows/docker-publish.yml/badge.svg?event=push)](https://github.com/fedor-git/wg-portal-2/actions/workflows/docker-publish.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](https://opensource.org/licenses/MIT)
![GitHub last commit](https://img.shields.io/github/last-commit/fedor-git/wg-portal-2/master)
[![Go Report Card](https://goreportcard.com/badge/github.com/fedor-git/wg-portal-2)](https://goreportcard.com/report/github.com/fedor-git/wg-portal-2)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/fedor-git/wg-portal-2)
![GitHub code size in bytes](https://img.shields.io/github/languages/code-size/fedor-git/wg-portal-2)
[![Forked from original-repo](https://img.shields.io/badge/Forked%20from-h44z%2Fwg--portal-blue?logo=github)](https://github.com/h44z/wg-portal)

## Recent Enhancements

This fork includes several important improvements:

### 🚀 Cluster Mode Support
Run multiple instances with a shared database. Synchronization is handled by the **fanout module** for seamless peer and interface updates across all nodes.

### 🔧 Configuration Enhancements
New core configuration options for fine-grained control over DNS, routing, and peer lifecycle management.

## Introduction
<!-- Text from this line # is included in docs/documentation/overview.md -->
**WireGuard Portal** is a simple, web-based configuration portal for [WireGuard](https://wireguard.com) server management.
The portal uses the WireGuard [wgctrl](https://github.com/WireGuard/wgctrl-go) library to manage existing VPN
interfaces. This allows for the seamless activation or deactivation of new users without disturbing existing VPN
connections.

The configuration portal supports using a database (SQLite, MySQL, MsSQL, or Postgres), OAuth or LDAP
(Active Directory or OpenLDAP) as a user source for authentication and profile data.

## Features

* Self-hosted - the whole application is a single binary
* Responsive multi-language web UI written in Vue.js
* Automatically selects IP from the network pool assigned to the client
* QR-Code for convenient mobile client configuration
* Sends email to the client with QR-code and client config
* Enable / Disable clients seamlessly
* Generation of wg-quick configuration file (`wgX.conf`) if required
* User authentication (database, OAuth, or LDAP), Passkey support
* IPv6 ready
* Docker ready
* Can be used with existing WireGuard setups
* Support for multiple WireGuard interfaces
* Supports multiple WireGuard backends (wgctrl or MikroTik [BETA])
* Peer Expiry Feature
* Handles route and DNS settings like wg-quick does
* Exposes Prometheus metrics for monitoring and alerting
* REST API for management and client deployment
* Webhook for custom actions on peer, interface, or user updates

<!-- Text to this line # is included in docs/documentation/overview.md -->
![Screenshot](docs/assets/images/screenshot.png)

## Configuration Guide

### Core Options

The latest version introduces several new core configuration options for production deployments:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `admin_user` | string | - | Administrator username/email for initial setup |
| `admin_password` | string | - | Administrator password (hashed in database) |
| `admin_api_token` | string | - | API token for admin operations and cluster auth |
| `restore_state` | boolean | `false` | Restore WireGuard interface state from database on startup |
| `import_existing` | boolean | `false` | Import existing WireGuard configuration |
| `manage_dns` | boolean | `true` | Enable/disable automatic DNS management (resolv.conf). Set to `false` if managing DNS externally. |
| `ignore_main_default_route` | boolean | `false` | When `true`, the main route table won't be modified. Useful in container or complex networking setups. |
| `delete_expired_peers` | boolean | `false` | Automatically remove peers after their TTL expires. |
| `default_user_ttl` | string/integer | `0` | Default Time-To-Live for peers (e.g., `30d`, `1` for 1 day). `0` means no expiration. |
| `sync_on_startup` | boolean | `false` | Synchronize interface state with database on startup |
| `force_client_ip_as_allowed_ip` | boolean | `false` | Use client IP as allowed IP instead of calculating from pool |
| `master` | boolean | `false` | Mark this instance as master (for failover scenarios) |

### Cluster Mode with Fanout Module

The **Fanout module** enables synchronization across multiple WireGuard Portal instances sharing a single database. This is essential for high-availability deployments.

#### How It Works

1. Each instance publishes events (peer updates, deletions, interface changes) to other instances
2. All instances listen for updates and apply changes locally
3. Debouncing prevents thundering herd problems
4. Automatic peer kicking on startup ensures consistency

#### Fanout Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | boolean | `false` | Enable cluster synchronization |
| `self_url` | string | - | This instance's URL (supports `{{ external_url }}` template) |
| `peers` | array | - | List of peer node URLs for cluster communication |
| `auth_header` | string | `Authorization` | HTTP header name for authentication |
| `auth_value` | string | - | Auth value (e.g., `Basic base64{user:token}`) |
| `timeout` | duration | `2s` | Request timeout for peer communication |
| `debounce` | duration | `300ms` | Time to wait before batching events |
| `origin` | string | `wg-portal` | Optional identifier for event origin |
| `kick_on_start` | boolean | `true` | Remove stale entries on startup |
| `tls_skip_verify` | boolean | `false` | Skip TLS verification for peer URLs (NOT for production!) |
| `topics` | array | all | Event topics to synchronize |

#### Configuration Example

```yaml
core:
  admin_user: username@example.com
  admin_password: ...
  restore_state: false
  import_existing: false
  admin_api_token: ...
  manage_dns: false
  ignore_main_default_route: true
  delete_expired_peers: true
  default_user_ttl: 1d  # 1 days default expiration
  sync_on_startup: true
  force_client_ip_as_allowed_ip: true
  master: true

  fanout:
    enabled: true
    self_url: "https://wg-portal-1.example.com"  # Use template variable: {{ external_url }}
    # List of other cluster nodes
    peers:
      - "https://wg-portal-2.example.com"
      - "https://wg-portal-3.example.com"

    # Communication settings
    auth_header: "Authorization"
    auth_value: "Basic base64{admin_user:admin_api_token}"
    timeout: 2s
    debounce: 300ms  # Wait 300ms before processing event batch
    origin: "wg-portal"  # Optional origin identifier
    kick_on_start: true  # Remove stale peer entries on startup
    
    # Event topics to synchronize (all shown by default)
    topics:
      - "peers.updated"    # When peer list is updated
      - "peer.save"        # Individual peer saved
      - "peer.delete"      # Peer deleted
      - "interface.save"   # Interface configuration changed
      - "interface.updated"# Interface state changed
    tls_skip_verify: true

advanced:
  config_storage_path: /etc/wireguard/
  rule_prio_offset: 32000
  log_level: info
  log_pretty: true
  log_json: false
  expiry_check_interval: 5h

web:
  listening_address: :8888
  external_url: https://wg-portal-1.example.com:4848
  request_logging: true
  csrf_secret: ...
  session_secret: ...

statistics:
  use_ping_checks: false
  ping_check_workers: 3
  ping_unprivileged: false
  ping_check_interval: 5m
  data_collection_interval: 60s
  collect_interface_data: true
  collect_peer_data: true
  collect_audit_data: true
  store_audit_data: false
  listening_address: ":8787"
  export_detailed_peer_metrics: false
  only_export_connected_peers: true
```

#### Deployment Architecture

```
                ┌───────────────────────────┐
                │   Shared Database         │
                │   MySQL/PostgreSQL        │
                └─────────────┬─────────────┘
                              │
            ┌─────────────────┼──────────────────┐
            │                 │                  │
        ┌───▼────┐        ┌───▼────┐        ┌────▼───┐
        │ Node 1 │◄──────►│ Node 2 │◄──────►│ Node 3 │
        │ Portal │        │ Portal │        │ Portal │
        └───┬────┘        └───┬────┘        └────┬───┘
            │                 │                  │
          [wg0]             [wg0]              [wg0]
            │                 │                  │
        ┌───┴─────────────────┼──────────────────┴───┐
        │  Network / Load Balancer (HAProxy/nginx)   │
        └────────────────────────────────────────────┘
```

### DNS Management

When `manage_dns: false`, the portal won't modify `/etc/resolv.conf`. This is useful when:
- Running in containers with custom DNS configuration
- Using external DNS management tools
- DNS is configured via cloud orchestration (Kubernetes, etc.)

### Route Management

Setting `ignore_main_default_route: true` prevents modification of the main route table. This is necessary for:
- Complex multi-interface hosts
- Containers with restricted capabilities
- Environments where routing is managed externally

### Peer Expiration

Configure automatic peer cleanup:

```yaml
core:
  delete_expired_peers: true
  default_user_ttl: 30d  # 30 days expiration
```

Individual peers can also have custom TTL values. After expiration:
1. Peer status changes to "expired"
2. Peer is automatically removed if `delete_expired_peers: true`
3. WireGuard configuration is updated automatically

### Advanced Options

Fine-tune WireGuard configuration and logging:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `config_storage_path` | string | `/etc/wireguard/` | Directory where WireGuard config files are stored |
| `rule_prio_offset` | integer | `32000` | Base priority offset for iptables rules |
| `log_level` | string | `info` | Logging level: `debug`, `info`, `warn`, `error` |
| `log_pretty` | boolean | `false` | Pretty-print logs (human-readable format) |
| `log_json` | boolean | `false` | Output logs as JSON for log aggregation |
| `expiry_check_interval` | duration | `5h` | How often to check for expired peers |

### Web Interface Options

Configure the web portal:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `listening_address` | string | `:8080` | HTTP listening address |
| `external_url` | string | - | External URL for QR codes and API responses |
| `request_logging` | boolean | `true` | Log HTTP requests |
| `csrf_secret` | string | - | Secret key for CSRF protection (auto-generated if not set) |
| `session_secret` | string | - | Secret key for session encryption (auto-generated if not set) |

### Statistics & Monitoring Options

Configure metrics collection and reporting:

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `use_ping_checks` | boolean | `false` | Enable ping-based peer availability checks |
| `ping_check_workers` | integer | `3` | Number of concurrent ping workers |
| `ping_unprivileged` | boolean | `false` | Use unprivileged ping (requires ICMP capabilities) |
| `ping_check_interval` | duration | `5m` | Interval between ping checks |
| `data_collection_interval` | duration | `60s` | How often to collect statistics |
| `collect_interface_data` | boolean | `true` | Collect interface metrics (traffic, handshakes) |
| `collect_peer_data` | boolean | `true` | Collect peer-level metrics |
| `collect_audit_data` | boolean | `false` | Collect audit trail data |
| `store_audit_data` | boolean | `false` | Persist audit data to database |
| `listening_address` | string | `:8787` | Prometheus metrics listening address |
| `export_detailed_peer_metrics` | boolean | `false` | Export per-peer metrics (can be expensive) |
| `only_export_connected_peers` | boolean | `true` | Only export metrics for connected peers |

## Documentation

For the complete documentation visit [wgportal.org](https://wgportal.org).

## What is out of scope

* Automatic generation or application of any `iptables` or `nftables` rules.
* Support for operating systems other than linux.
* Automatic import of private keys of an existing WireGuard setup.

## Quick Start with Cluster Mode

### Prerequisites

- MySQL 5.7+ or PostgreSQL 10+ (shared database)
- Linux kernel with WireGuard support
- 3+ instances for HA setup (optional, 1 instance works fine for single-node)

### Docker Cluster Deployment

```bash
# Create shared network
docker network create wg-portal

# Start database
docker run -d \
  --name mysql-wg \
  --network wg-portal \
  -e MYSQL_ROOT_PASSWORD=secret \
  mysql:8.0

# Wait for DB to be ready
sleep 10

# Start first portal node
docker run -d \
  --name wg-portal-1 \
  --network wg-portal \
  --cap-add=NET_ADMIN \
  -e PORTAL_LISTEN_ADDRESS=0.0.0.0:8080 \
  -e PORTAL_EXTERNAL_URL=https://wg-portal-1.example.com \
  wgportal/wg-portal

# Start second portal node
docker run -d \
  --name wg-portal-2 \
  --network wg-portal \
  --cap-add=NET_ADMIN \
  -e PORTAL_LISTEN_ADDRESS=0.0.0.0:8080 \
  -e PORTAL_EXTERNAL_URL=https://wg-portal-2.example.com \
  wgportal/wg-portal
```

### Verify Cluster Communication

```bash
# Check logs for fanout initialization
docker logs wg-portal-1 | grep -i fanout

# Test peer synchronization
# Create/modify peer on node 1, should appear on node 2 within debounce time
```

## Testing & Validation

The cluster mode has been tested with:
- ✅ MySQL 5.7, 8.0+
- ✅ PostgreSQL 12+
- ✅ Up to 5 concurrent nodes
- ✅ Load balancer (HAProxy, nginx)
- ✅ Automatic failover scenarios

## Application stack

* [wgctrl-go](https://github.com/WireGuard/wgctrl-go) and [netlink](https://github.com/vishvananda/netlink) for interface handling
* [Bootstrap](https://getbootstrap.com/), for the HTML templates
* [Vue.js](https://vuejs.org/), for the frontend

## License

* MIT License. [MIT](LICENSE.txt) or <https://opensource.org/licenses/MIT>


> [!IMPORTANT]
> Since the project was accepted by the Docker-Sponsored Open Source Program, the Docker image location has moved to [wgportal/wg-portal](https://hub.docker.com/r/wgportal/wg-portal).
> Please update the Docker image from **fedor-git/wg-portal-2** to **wgportal/wg-portal**.
