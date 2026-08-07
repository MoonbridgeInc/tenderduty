# Setting up Grafana

A ready-to-import dashboard is available at
[grafana-dashboard.json](grafana-dashboard.json), covering everything the
[Prometheus exporter](prometheus.md) exposes: consensus health (consecutive misses,
slashing-window usage, stall detection, empty blocks), RPC node health (lag, downtime,
monitored vs unhealthy endpoint counts), block production, and unvoted governance
proposals — with a `Chain` dropdown to filter down to one or more validators.

#### 1. Make sure Prometheus is scraping tenderduty
`prometheus_enabled: yes` in `config.yml` (see [Configuration Settings](config.md)) exposes
metrics on `prometheus_listen_port` (default `28686`) at `/metrics`. Point a Prometheus
server at it, e.g.:
```yaml
scrape_configs:
  - job_name: tenderduty
    static_configs:
      - targets: ["localhost:28686"]
```

#### 2. Add Prometheus as a Grafana data source
In Grafana: **Connections → Data sources → Add data source → Prometheus**, point it at
your Prometheus server's URL, save & test.

#### 3. Import the dashboard
**Dashboards → New → Import**, upload [grafana-dashboard.json](grafana-dashboard.json) (or
paste its contents), select the Prometheus data source you just created, click **Import**.

The dashboard's `Datasource` variable is what you select during import; the `Chain`
variable is populated automatically from the `name` label already present on every
tenderduty metric, so no further editing is needed.
