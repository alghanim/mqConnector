#!/usr/bin/env bash
# Produce a release tarball containing the binary, example config, systemd
# unit, and install script. The tarball is named mqconnector-<version>.tar.gz.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="$(cat VERSION)"
WITH_IBMMQ="${WITH_IBMMQ:-0}"

if [[ "$WITH_IBMMQ" == "1" ]]; then
  ./scripts/build.sh --ibmmq
else
  ./scripts/build.sh
fi

STAGE="dist/mqconnector-${VERSION}"
rm -rf "$STAGE"
mkdir -p "$STAGE"

cp dist/mqconnector "$STAGE/"
cp config.example.yaml "$STAGE/"
cp README.md COMPLIANCE.md BRAND-COMPLIANCE.md VERSION "$STAGE/"

cat > "$STAGE/mqconnector.service" <<'SERVICE'
[Unit]
Description=mqConnector — message-queue bridge
After=network.target

[Service]
Type=simple
User=mqconnector
Group=mqconnector
WorkingDirectory=/opt/mqconnector
ExecStart=/opt/mqconnector/mqconnector -config /etc/mqconnector/config.yaml
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/mqconnector
PrivateTmp=true

[Install]
WantedBy=multi-user.target
SERVICE

cat > "$STAGE/install.sh" <<'INSTALL'
#!/usr/bin/env bash
# Install mqConnector under /opt/mqconnector with a systemd unit. Run as root.
set -euo pipefail

if [[ $EUID -ne 0 ]]; then echo "Run as root" >&2; exit 1; fi

id mqconnector &>/dev/null || useradd --system --no-create-home --shell /usr/sbin/nologin mqconnector

install -d -o mqconnector -g mqconnector /opt/mqconnector /var/lib/mqconnector /etc/mqconnector
install -m 0755 -o root -g root mqconnector /opt/mqconnector/mqconnector

if [[ ! -f /etc/mqconnector/config.yaml ]]; then
  install -m 0640 -o mqconnector -g mqconnector config.example.yaml /etc/mqconnector/config.yaml
  echo "Wrote /etc/mqconnector/config.yaml (review before starting)"
fi

install -m 0644 mqconnector.service /etc/systemd/system/mqconnector.service
systemctl daemon-reload
echo "Done. Enable and start with:"
echo "  systemctl enable --now mqconnector"
INSTALL
chmod +x "$STAGE/install.sh"

tar -C dist -czf "dist/mqconnector-${VERSION}.tar.gz" "mqconnector-${VERSION}"
echo "✓ dist/mqconnector-${VERSION}.tar.gz"
