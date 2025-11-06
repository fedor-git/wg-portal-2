By default, WG-Portal exposes Prometheus metrics on port `8787` if interface/peer statistic data collection is enabled.

## Configuration

You can control which metrics are exported using the `statistics.export_detailed_peer_metrics` configuration option:

```yaml
statistics:
  export_detailed_peer_metrics: false  # Default: true
```

- **`true` (default)**: Export all metrics (interface metrics + all peer metrics)
- **`false`**: Export only essential metrics (`wireguard_peer_up` and interface metrics)

Setting this to `false` can significantly reduce the number of metrics exported, which is useful for:
- Large deployments with many peers
- Reducing Prometheus storage requirements
- Minimizing network bandwidth for metric scraping
- When you only need to monitor peer connection status

## Exposed Metrics

### Interface Metrics (always exported)

| Metric                                     | Type  | Description                                    |
|--------------------------------------------|-------|------------------------------------------------|
| `wireguard_interface_received_bytes_total` | gauge | Bytes received through the interface.          |
| `wireguard_interface_sent_bytes_total`     | gauge | Bytes sent through the interface.              |

### Peer Metrics (always exported)

| Metric                                     | Type  | Description                                    |
|--------------------------------------------|-------|------------------------------------------------|
| `wireguard_peer_up`                        | gauge | Peer connection state (boolean: 1/0).          |

### Detailed Peer Metrics (only when `export_detailed_peer_metrics: true`)

| Metric                                     | Type  | Description                                    |
|--------------------------------------------|-------|------------------------------------------------|
| `wireguard_peer_last_handshake_seconds`    | gauge | Seconds from the last handshake with the peer. |
| `wireguard_peer_received_bytes_total`      | gauge | Bytes received from the peer.                  |
| `wireguard_peer_sent_bytes_total`          | gauge | Bytes sent to the peer.                        |

## Prometheus Config

Add the following scrape job to your Prometheus config file:

```yaml
# prometheus.yaml
scrape_configs:
  - job_name: wg-portal
    scrape_interval: 60s
    static_configs:
      - targets:
          - localhost:8787 # Change localhost to IP Address or hostname with WG-Portal
```

# Grafana Dashboard

You may import [`dashboard.json`](https://github.com/fedor-git/wg-portal-2/blob/master/deploy/helm/files/dashboard.json) into your Grafana instance.

![Dashboard](../../assets/images/dashboard.png)
