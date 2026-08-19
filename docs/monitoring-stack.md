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

Config — scrapes tenderduty and node_exporter locally. The `relabel_configs` blocks
override the `instance` label from the default `host:port` (e.g. `localhost:9100`,
meaningless in an alert notification) to a readable server name — replace
`your-server-name` with whatever makes sense to you; it'll show up everywhere `instance`
does, not just alert messages, including the Node Exporter Full dashboard from step 5:

```bash
sudo tee /etc/prometheus/prometheus.yml << 'EOF'
global:
  scrape_interval: 15s

scrape_configs:
  - job_name: tenderduty
    static_configs:
      - targets: ["localhost:28686"]
    relabel_configs:
      - target_label: instance
        replacement: your-server-name

  - job_name: node_exporter
    static_configs:
      - targets: ["localhost:9100"]
    relabel_configs:
      - target_label: instance
        replacement: your-server-name
      # Stable label the "node_exporter down" alert rule (step 7) matches on,
      # independent of job_name - see the gotcha in step 3 about why job_name
      # itself isn't safe to alert on.
      - target_label: exporter
        replacement: node_exporter
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
picked it up: open `http://localhost:9090/targets` (over an SSH tunnel, see step 6) and
check both `tenderduty` and `node_exporter` jobs show **UP**.

## 3. Adding another server (optional)

To monitor more than one machine from this same Prometheus/Grafana, install node_exporter
on each additional server and add it as its own scrape target — no need for a second
Prometheus or Grafana.

On the **new** server, same node_exporter install as step 2, except it needs to listen on
more than just localhost (Prometheus is reaching in from a different machine):
```bash
ExecStart=/usr/local/bin/node_exporter --web.listen-address=0.0.0.0:9100
```

**Firewall this before anything else** — node_exporter has no authentication of its own.
If the new server's own firewall is `ufw`, restrict it to only the Prometheus server's IP:
```bash
sudo ufw allow from <PROMETHEUS_SERVER_IP> to any port 9100 proto tcp
```
If your hosting provider manages firewalling separately (a cloud firewall / security group,
not `ufw` on the box itself), add the equivalent rule there instead — TCP 9100, source =
the Prometheus server's IP only, never "anywhere."

On the **Prometheus server**, add a new job to `prometheus.yml`. Give it its own,
genuinely distinguishing `instance` name — not a generic category like "nodes" or
"services". If you ever add a third server, a vague shared name like that stops telling
you which machine an alert is actually about:
```yaml
  - job_name: node_exporter_<short-name-for-this-server>
    static_configs:
      - targets: ["<NEW_SERVER_IP>:9100"]
    relabel_configs:
      - target_label: instance
        replacement: <a-real-distinguishing-name>
      # Same stable label as the first server's job (step 2) - keep this on every
      # node_exporter job you ever add, regardless of what you name job_name.
      - target_label: exporter
        replacement: node_exporter
```
```bash
sudo systemctl restart prometheus
```

Nothing else to do — the Node Exporter Full dashboard's `instance`/`nodename` pickers
already apply to *any* matching instance (it doesn't filter by instance), so the new
server is automatically covered there. The Disk/Memory/Load Telegram alert rules from
step 7 are the same, since they query raw metric names with no job filter at all - but
**"node_exporter down" specifically matches on the `exporter` label above, not
`job_name`** (see the gotcha right below for why), so as long as this server's job block
carries that label too, it's covered automatically.

**Gotcha, if you rename a `job_name` later:** Prometheus doesn't retroactively rename old
data — samples already stored under the old name stay queryable (within your retention
window) under that old label. Grafana's dropdowns populate from *any* label value seen in
the selected time range, so a renamed job keeps showing up as a stale, no-longer-scraped
"ghost" option for a while. Harmless, not a bug — it drops out on its own once those old
samples age out of whatever time range the dashboard is showing (or past
`storage.tsdb.retention.time` entirely).

**Bigger gotcha — an alert rule that queries by `job=` breaks silently on a rename.**
Unlike the dropdown case above, this one isn't harmless: `up{job="node_exporter"}` (an
early draft of the "node_exporter down" rule below) stops matching *anything* the moment
you rename a `job_name`, and Prometheus/Grafana don't error on that — the query just
returns zero series forever. Grafana then evaluates the rule as **NoData** and keeps
re-notifying on its normal repeat interval (every few hours), showing up in Telegram as a
`DatasourceNoData` alert with blank labels and `Value: 0.00` that never resolves on its
own. This is exactly why the job blocks above carry the separate `exporter: node_exporter`
label and the alert rule matches on *that* instead of `job` - rename `job_name` as many
times as you want afterward, `exporter` never changes. If you already have a
`node_exporter down` rule written against `job="node_exporter"` and it's firing NoData:
add the `exporter` relabel to every existing node_exporter job block, restart Prometheus,
then update the rule's query to match step 7 below.

## 4. Install Grafana

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

## 5. Add Prometheus as a Grafana data source, import the dashboards

Once you can reach Grafana (see step 6 for exposing it), follow
[Setting up Grafana](grafana.md) steps 2–3 to add the Prometheus data source and import
`grafana-dashboard.json` for tenderduty.

For node_exporter, Grafana.com has a well-known community dashboard covering it in full —
**Dashboards → New → Import**, dashboard ID **1860** ("Node Exporter Full"), pick the same
Prometheus data source.

## 6. Expose Grafana

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

## 7. Telegram alerting from Grafana

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

#### Evaluation groups
Alert rules run at the interval their *evaluation group* is set to, and pending period
can't be shorter than that interval — so two groups covers everything below:
- **`infra-alerts`** — interval `5m`. For anything that trends slowly (disk, memory,
  load) where a one-off blip shouldn't page you.
- **`infra-alerts-fast`** — interval `1m`. For binary up/down checks where you want to
  know quickly.

Create both once via **+ New evaluation group** the first time you need them; every rule
below just picks whichever already exists from the dropdown.

For every rule: query goes in **Code** mode (not the visual Builder — simpler to just
paste PromQL directly), and notifications route to the `telegram-infra` contact point
created above.

#### Disk filling up
- Query:
  ```
  100 - (node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"} * 100)
  ```
- Condition: `IS ABOVE 90`
- Evaluation group: `infra-alerts` — Pending period: `5m`

#### Memory low
- Query:
  ```
  node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes * 100
  ```
- Condition: `IS BELOW 10`
- Evaluation group: `infra-alerts` — Pending period: `5m`

#### High load average
- Query:
  ```
  node_load1 / scalar(count(count(node_cpu_seconds_total) by (cpu)))
  ```
  (`node_load1` divided by core count, so 1.0 = "fully busy"; the inner double-`count()`
  counts distinct cores, and the whole thing needs wrapping in `scalar()` — without it,
  the result carries no labels and won't match against `node_load1{instance=...}`, so the
  query silently returns no data.)
- Condition: `IS ABOVE 1.3` — deliberately *above* 1.0 (100% busy), not below. A validator
  box running several chains' full nodes will legitimately sit at or near 1.0 most of the
  time; that's normal, not a problem. Only alert once load meaningfully exceeds core count
  (processes genuinely queuing for CPU, not just "busy"). Tune based on your own box's
  baseline — check `htop`/`btop`'s load average against its core count first.
- Evaluation group: `infra-alerts` — Pending period: `5m`

#### node_exporter down
- Query:
  ```
  up{exporter="node_exporter"}
  ```
  Matches on the stable `exporter` label from steps 2/3, not `job` - a plain
  `job="node_exporter"` query breaks the moment any node_exporter job gets renamed (see
  the "bigger gotcha" in step 3) and silently turns into a permanently-firing NoData
  alert instead of an error.
- Condition: `IS BELOW 1`
- Evaluation group: `infra-alerts-fast` — Pending period: `1m`
- Caveat: this catches node_exporter crashing or a network blip to it — it does **not**
  catch the whole server going down, since Prometheus/Grafana live on the same box and
  can't alert on their own host being unreachable. True external uptime monitoring needs
  a checker running on a *different* machine (healthchecks.io, UptimeRobot, etc.).

#### tenderduty down
- Query:
  ```
  up{job="tenderduty"}
  ```
- Condition: `IS BELOW 1`
- Evaluation group: `infra-alerts-fast` — Pending period: `1m`
- Same caveat as above, plus: this only means Prometheus can't scrape tenderduty's
  `/metrics` — tenderduty's own `healthcheck.enabled` ([config.md](config.md)) is a
  separate, independent dead-man's-switch mechanism for the same underlying concern
  (tenderduty itself crashing), worth having both.

---

These five cover host + process liveness. Anything else queryable in Prometheus
(tenderduty's own metrics included, e.g. `tenderduty_consecutive_missed_blocks`) can
become a Grafana alert rule the same way. For pure validator-consensus alerting though,
prefer tenderduty's own built-in alerts ([config.md](config.md)) — they already understand
slashing windows, jailing, and double-signing far better than a generic PromQL threshold
would; treat Grafana alerting here as infra-level coverage, not a replacement.

## 8. Optional: Caddy metrics

If you're reverse-proxying through Caddy (see [deploy-ubuntu.md](deploy-ubuntu.md)), it
has built-in Prometheus metrics — request counts, latencies, status-code breakdowns per
site. They're served over Caddy's **admin API**, which `admin off` in the global options
block (a hardening choice from that guide) disables entirely.

In the Caddyfile's global options block:
```caddyfile
{
	# admin off        <- remove this line
	metrics            # global option — the form nested inside `servers { }` still
	                    # works in some versions but is deprecated
	...
}
```
The admin API still only binds to `127.0.0.1:2019` by default — same "not reachable from
outside this box unless you proxy it yourself" exposure as Prometheus/node_exporter.

```bash
caddy validate --config /etc/caddy/Caddyfile
sudo systemctl restart caddy   # a full restart, not reload — global options need it
curl -s localhost:2019/metrics | grep "^caddy_http"   # confirms request metrics exist
```

Add the scrape target to `prometheus.yml`:
```yaml
  - job_name: caddy
    static_configs:
      - targets: ["localhost:2019"]
    metrics_path: /metrics
    relabel_configs:
      - target_label: instance
        replacement: your-server-name
```
```bash
sudo systemctl restart prometheus
```

Import the dashboard: **Dashboards → New → Import** → ID **22870** ("Caddy") → pick the
Prometheus data source.

## Updating later

```bash
# Prometheus / node_exporter: repeat the download+copy steps above with the new version,
# then:
sudo systemctl restart prometheus
sudo systemctl restart node_exporter
# ...and node_exporter on every additional server from step 3, each on its own box

# Grafana: it's a normal apt package
sudo apt-get update && sudo apt-get upgrade -y grafana
sudo systemctl restart grafana-server
```
