# Deploying Prometheus + node_exporter + Grafana + Telegram alerts

This sets up a full monitoring stack alongside tenderduty on the same Ubuntu server:

- **Prometheus** — scrapes tenderduty's exporter ([prometheus.md](prometheus.md)) and
  node_exporter, stores the time series.
- **node_exporter** — exposes *host*-level metrics (disk, CPU, memory, network). Tenderduty
  only watches validator/chain state — if the server itself runs low on disk or memory,
  nothing in tenderduty would ever tell you. This is what covers that gap.
- **Grafana** — visualizes both. Import [grafana-dashboard.json](grafana-dashboard.json)
  for tenderduty ([Setting up Grafana](grafana.md)) and a community dashboard for
  node_exporter.
- **Telegram alerts** — this is a *second, independent* alerting layer from tenderduty's
  own built-in Telegram alerts ([Setting up Telegram](telegram.md)). Tenderduty alerts on
  validator-specific conditions (missed blocks, jailing, etc.); Grafana alerting here is
  for infra-level conditions (disk filling up, high load) or any custom PromQL threshold
  you want. They can post to the same Telegram channel or separate ones — your call.

## 1. Install Prometheus

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin prometheus
sudo mkdir -p /etc/prometheus /var/lib/prometheus

# fetch the latest release automatically instead of hardcoding a version that'll go stale
PROM_VERSION=$(curl -s https://api.github.com/repos/prometheus/prometheus/releases/latest | grep -oP '"tag_name": "v\K[^"]+')
curl -fsSL "https://github.com/prometheus/prometheus/releases/download/v${PROM_VERSION}/prometheus-${PROM_VERSION}.linux-amd64.tar.gz" -o /tmp/prometheus.tar.gz
tar -xzf /tmp/prometheus.tar.gz -C /tmp
sudo cp /tmp/prometheus-${PROM_VERSION}.linux-amd64/prometheus /tmp/prometheus-${PROM_VERSION}.linux-amd64/promtool /usr/local/bin/
sudo chown -R prometheus:prometheus /etc/prometheus /var/lib/prometheus
```

Config — scrapes tenderduty and node_exporter locally:

```bash
sudo tee /etc/prometheus/prometheus.yml << 'EOF'
global:
  scrape_interval: 15s

scrape_configs:
  - job_name: tenderduty
    static_configs:
      - targets: ["localhost:28686"]

  - job_name: node_exporter
    static_configs:
      - targets: ["localhost:9100"]
EOF
sudo chown prometheus:prometheus /etc/prometheus/prometheus.yml
```

Service:

```bash
sudo tee /etc/systemd/system/prometheus.service << 'EOF'
[Unit]
Description=Prometheus
After=network.target

[Service]
Type=simple
User=prometheus
Restart=always
RestartSec=5
ExecStart=/usr/local/bin/prometheus \
  --config.file=/etc/prometheus/prometheus.yml \
  --storage.tsdb.path=/var/lib/prometheus \
  --storage.tsdb.retention.time=30d \
  --web.listen-address=127.0.0.1:9090

NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/prometheus
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now prometheus
sudo systemctl status prometheus --no-pager
```

`--web.listen-address=127.0.0.1:9090` binds Prometheus to localhost only — it's not meant
to be reachable from outside this box, only by Grafana running on the same host. 30 days
of retention is a reasonable default for a single small server; raise it if you have disk
to spare.

## 2. Install node_exporter

Same pattern, its own dedicated user:

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin node_exporter

NODE_EXPORTER_VERSION=$(curl -s https://api.github.com/repos/prometheus/node_exporter/releases/latest | grep -oP '"tag_name": "v\K[^"]+')
curl -fsSL "https://github.com/prometheus/node_exporter/releases/download/v${NODE_EXPORTER_VERSION}/node_exporter-${NODE_EXPORTER_VERSION}.linux-amd64.tar.gz" -o /tmp/node_exporter.tar.gz
tar -xzf /tmp/node_exporter.tar.gz -C /tmp
sudo cp /tmp/node_exporter-${NODE_EXPORTER_VERSION}.linux-amd64/node_exporter /usr/local/bin/
```

```bash
sudo tee /etc/systemd/system/node_exporter.service << 'EOF'
[Unit]
Description=Prometheus Node Exporter
After=network.target

[Service]
Type=simple
User=node_exporter
Restart=always
RestartSec=5
ExecStart=/usr/local/bin/node_exporter --web.listen-address=127.0.0.1:9100

NoNewPrivileges=true
ProtectSystem=strict
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now node_exporter
curl -s localhost:9100/metrics | head -5   # sanity check
```

Also bound to `127.0.0.1` only — same reasoning as Prometheus itself. Confirm Prometheus
picked it up: open `http://localhost:9090/targets` (over an SSH tunnel, see step 5) and
check both `tenderduty` and `node_exporter` jobs show **UP**.

## 3. Install Grafana

Via the official APT repo (keeps itself updatable with normal `apt upgrade`, no manual
version tracking):

```bash
sudo apt-get install -y apt-transport-https software-properties-common wget
sudo mkdir -p /etc/apt/keyrings
wget -q -O - https://apt.grafana.com/gpg.key | gpg --dearmor | sudo tee /etc/apt/keyrings/grafana.gpg > /dev/null
echo "deb [signed-by=/etc/apt/keyrings/grafana.gpg] https://apt.grafana.com stable main" | sudo tee /etc/apt/sources.list.d/grafana.list

sudo apt-get update
sudo apt-get install -y grafana

sudo systemctl enable --now grafana-server
sudo systemctl status grafana-server --no-pager
```

Grafana listens on `127.0.0.1:3000` by default in a standard install — not exposed
publicly yet. First login is `admin`/`admin`; it'll force a password change immediately.

## 4. Add Prometheus as a Grafana data source, import the dashboards

Once you can reach Grafana (see step 5 for exposing it), follow
[Setting up Grafana](grafana.md) steps 2–3 to add the Prometheus data source and import
`grafana-dashboard.json` for tenderduty.

For node_exporter, Grafana.com has a well-known community dashboard covering it in full —
**Dashboards → New → Import**, dashboard ID **1860** ("Node Exporter Full"), pick the same
Prometheus data source.

## 5. Expose Grafana

Unlike tenderduty before `dashboard_auth` existed, Grafana always requires its own login
out of the box — putting it behind Caddy with just TLS is reasonably safe. Add to the
Caddyfile, matching the same pattern as the other `*.example.com` blocks:

```
grafana.example.com {
	import log_standard grafana
	import security_headers_base
	reverse_proxy localhost:3000 {
		transport http {
			read_timeout  60s
			write_timeout 60s
			dial_timeout  5s
		}
	}
}
```

Then `caddy validate` + `sudo systemctl reload caddy`, same as always. If you'd rather not
expose it publicly at all yet, tunnel over SSH instead:

```bash
ssh -L 3000:localhost:3000 -L 9090:localhost:9090 user@your-server
# then browse http://localhost:3000 (Grafana) and http://localhost:9090 (Prometheus) locally
```

(The Prometheus tunnel above is only for checking `/targets`/running ad-hoc queries — don't
put Prometheus itself behind Caddy, it has no auth of its own.)

## 6. Telegram alerting from Grafana

You can reuse the exact same bot/channel from [Setting up Telegram](telegram.md) if you
already made one for tenderduty's own alerts, or make a second bot/channel to keep infra
alerts separate from validator alerts — either works, it's just a contact point.

#### Contact point
**Alerting → Contact points → Add contact point**:
- Name: e.g. `telegram-infra`
- Integration: **Telegram**
- BOT API Token: the token from @BotFather
- Chat ID: same channel-ID lookup as in [telegram.md](telegram.md) step 4

Save, then **Test** — you should get a message in the channel immediately.

#### Example alert rule: disk filling up
**Alerting → Alert rules → New alert rule**:
- Query (Prometheus data source):
  ```
  100 - (node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"} * 100)
  ```
- Condition: `IS ABOVE 90` (fires when root filesystem is over 90% full)
- Evaluation: every `1m`, for `5m` (avoids a one-off blip firing an alert)
- Labels/notifications: route to a notification policy pointing at the `telegram-infra`
  contact point created above (or set it directly as the contact point for this rule)

This is just one example — anything you can query in Prometheus (tenderduty's own metrics
included, e.g. `tenderduty_consecutive_missed_blocks`) can become a Grafana alert rule the
same way. For pure validator-consensus alerting though, prefer tenderduty's own built-in
alerts ([config.md](config.md)) — they already understand slashing windows, jailing, and
double-signing far better than a generic PromQL threshold would.

## Updating later

```bash
# Prometheus / node_exporter: repeat the download+copy steps above with the new version,
# then:
sudo systemctl restart prometheus
sudo systemctl restart node_exporter

# Grafana: it's a normal apt package
sudo apt-get update && sudo apt-get upgrade -y grafana
sudo systemctl restart grafana-server
```
