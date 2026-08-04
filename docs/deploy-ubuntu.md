# Deploying to an Ubuntu server (Moonbridge fork)

Build-from-source + systemd, running the standalone binary as a persistent
service with auto-restart and `journalctl` logging. Tested against Ubuntu
26.04, but nothing here is version-specific.

## 1. Install Go

The official tarball is more reliable than `apt`/`snap` for getting a Go
version that satisfies this repo's `go.mod` (>= 1.21):

```bash
curl -fsSL https://go.dev/dl/go1.23.4.linux-amd64.tar.gz -o /tmp/go.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc
source ~/.bashrc
go version
```

## 2. Clone and build

```bash
git clone https://github.com/MoonbridgeInc/tenderduty.git
cd tenderduty
go build -ldflags '-s -w' -trimpath -o tenderduty main.go
```

The resulting `tenderduty` binary is self-contained (the dashboard's static
assets are embedded at build time) — no need to ship the rest of the repo
alongside it.

## 3. Bring over your config

Copy your existing, already-tuned `config.yml` from wherever you were
testing it — **don't** start from `example-config.yml` on the server, you'd
lose your chain list and settings (`digest_interval_seconds`,
`escalation_minutes`, notification channels, etc.):

```bash
# from your local machine
scp /path/to/config.yml user@your-server:/path/to/tenderduty/config.yml
```

Sanity-check it starts cleanly before wiring up the service:

```bash
./tenderduty -f config.yml
# Ctrl+C once you've confirmed the dashboard comes up and chains connect
```

## 4. Run as a systemd service

```bash
sudo useradd --system --home /var/lib/tenderduty --create-home tenderduty
sudo cp tenderduty /var/lib/tenderduty/tenderduty
sudo cp config.yml /var/lib/tenderduty/config.yml
sudo chown -R tenderduty:tenderduty /var/lib/tenderduty

sudo tee /etc/systemd/system/tenderduty.service << 'EOF'
[Unit]
Description=Tenderduty
After=network.target

[Service]
Type=simple
Restart=always
RestartSec=5
User=tenderduty
WorkingDirectory=/var/lib/tenderduty
ExecStart=/var/lib/tenderduty/tenderduty -f /var/lib/tenderduty/config.yml

# there may be a large number of network connections if monitoring a lot of chains
LimitNOFILE=infinity

# extra process isolation
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/tenderduty
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now tenderduty

# watch it come up; Ctrl-C to stop watching (the service keeps running)
sudo journalctl -fu tenderduty
```

## 5. Dashboard exposure — read before opening a firewall port

The dashboard (port `8888` by default) has **no authentication**. This is a
deliberate, current trade-off, not an oversight — but it means anyone who can
reach the port can view validator details *and* hit `POST /silence` to
suppress your alerts.

- Default / recommended: **do not** expose `8888` directly. Access it from
  the server itself, or tunnel over SSH:
  ```bash
  ssh -L 8888:localhost:8888 user@your-server
  # then browse to http://localhost:8888 locally
  ```
- If you need normal URL access, put a reverse proxy in front with basic
  auth on at least the state-changing endpoints, rather than exposing
  tenderduty directly. Note that `basic_auth` in the proxy alone isn't
  sufficient if the raw port is still reachable — someone could hit
  `http://server-ip:8888/silence` directly and skip the proxy entirely, so
  make sure `8888` isn't independently reachable (host firewall, or — as is
  often the case — already blocked at the hosting-provider level; check
  before assuming either way).

  Example with [Caddy](https://caddyserver.com/):
  ```
  tenderduty.yourdomain.com {
      @mutating path /silence* /unsilence*
      basic_auth @mutating {
          admin JDJhJDE0JGV4YW1wbGVoYXNoZXhhbXBsZQ==
      }

      reverse_proxy localhost:8888
  }
  ```
  Generate the password hash with `caddy hash-password` and swap it in above.
  This protects `/silence`/`/unsilence` specifically; drop the `@mutating`
  matcher from the `basic_auth` line if you want the whole dashboard
  (including `/state`/`/logs`) behind a login too.

The Prometheus exporter (`28686` by default) should generally stay closed to
the public internet too, or be restricted by source IP (e.g. to a Grafana
host / scraper), for the same reason.

## Updating later

```bash
cd tenderduty
git pull
go build -ldflags '-s -w' -trimpath -o tenderduty main.go
sudo cp tenderduty /var/lib/tenderduty/tenderduty
sudo systemctl restart tenderduty
sudo journalctl -fu tenderduty   # confirm it came back up cleanly
```
