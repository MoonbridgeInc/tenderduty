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
# `install` (not `cp`) replaces the file by swapping it out rather than writing
# in place, so it won't fail with "Text file busy" if an older tenderduty binary
# at this path happens to still be running (e.g. from a previous deploy).
sudo install -m 755 tenderduty /var/lib/tenderduty/tenderduty
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

**Recommended: turn on `dashboard_auth`.** Tenderduty has built-in HTTP Basic Auth for
the whole dashboard (viewing, `/silence`, `/unsilence`, and `/ws`), disabled by default.
Generate a bcrypt hash and enable it in `config.yml`:
```bash
./tenderduty -hash-password
# prompts for a password, prints a bcrypt hash — never store the plaintext password
```
```yaml
dashboard_auth:
  enabled: yes
  username: admin
  password_hash: "$2a$..." # paste the hash printed above
```
This works regardless of how you expose the port — reverse proxy, direct, tunnel — since
it's enforced by tenderduty itself, not the network path in front of it.

- Default / recommended if you'd rather not expose the port at all: **do not** expose
  `8888` directly. Access it from the server itself, or tunnel over SSH:
  ```bash
  ssh -L 8888:localhost:8888 user@your-server
  # then browse to http://localhost:8888 locally
  ```
- If you're already running a reverse proxy for the domain (e.g. Caddy), you can layer
  its own `basic_auth` on top of `dashboard_auth` as defense-in-depth — that just means
  two separate login prompts, which is fine, not required:
  ```
  tenderduty.yourdomain.com {
      basic_auth {
          admin JDJhJDE0JGV4YW1wbGVoYXNoZXhhbXBsZQ==
      }

      reverse_proxy localhost:8888
  }
  ```
  Generate that hash with `caddy hash-password` (a separate hash from tenderduty's own
  `-hash-password` — they're two independent layers, not shared credentials).

The Prometheus exporter (`28686` by default) has no auth of its own (it's meant for a
Prometheus/Grafana scraper, not a browser) — keep it closed to the public internet, or
restrict it by source IP.

## Updating later

```bash
cd tenderduty
git pull
go build -ldflags '-s -w' -trimpath -o tenderduty main.go
sudo install -m 755 tenderduty /var/lib/tenderduty/tenderduty
sudo systemctl restart tenderduty
sudo journalctl -fu tenderduty   # confirm it came back up cleanly
```
