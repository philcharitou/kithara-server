#!/usr/bin/env bash
# Sets up kithara-server on a fresh Ubuntu (22.04, 24.04) or Debian (12) server, with
# HTTPS through Caddy. Run it as root on the server:
#
#   curl -fsSLO https://raw.githubusercontent.com/philcharitou/kithara-server/main/deploy/deploy.sh
#   sudo bash deploy.sh --domain books.example.com
#
# Options:
#   --domain NAME     the address to serve (point its DNS A record here first). Without
#                     one, <this-server's-ip>.sslip.io is used, which needs no DNS.
#   --name NAME       the server's name in the apps (default: Kithara)
#   --restore FILE    a backup zip from Settings → Backups on another kithara-server:
#                     its accounts, progress, transcripts and settings come along
#   --dir DIR         where it lives (default: /opt/kithara)
#
# Run it again any time: it keeps what is there and only fills in what is missing.
set -euo pipefail

DIR=/opt/kithara
DOMAIN=""
NAME=""
RESTORE=""
REF="${KITHARA_REF:-main}" # where the compose file and Caddyfile come from, if not beside this script

while [ $# -gt 0 ]; do
  case "$1" in
    --domain) DOMAIN="$2"; shift 2 ;;
    --name) NAME="$2"; shift 2 ;;
    --restore) RESTORE="$2"; shift 2 ;;
    --dir) DIR="$2"; shift 2 ;;
    -h|--help) sed -n '2,17p' "$0"; exit 0 ;;
    *) echo "unknown option: $1 (see --help)" >&2; exit 2 ;;
  esac
done

say() { printf '\n\033[1m%s\033[0m\n' "$*"; }
die() { printf '\n\033[31m%s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "Run this as root (sudo bash deploy.sh ...)."
command -v apt-get >/dev/null || die "This script is for Ubuntu or Debian."
if [ -n "$RESTORE" ]; then
  [ -f "$RESTORE" ] || die "No backup at $RESTORE."
  RESTORE="$(cd "$(dirname "$RESTORE")" && pwd)/$(basename "$RESTORE")"
fi
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------------------------------------------------------------- Docker
if ! command -v docker >/dev/null || ! docker compose version >/dev/null 2>&1; then
  say "Installing Docker (from Docker's own apt repository)"
  . /etc/os-release
  apt-get update -qq
  apt-get install -y -qq ca-certificates curl >/dev/null
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL "https://download.docker.com/linux/${ID}/gpg" -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${ID} ${VERSION_CODENAME} stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -qq
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-compose-plugin >/dev/null
  systemctl enable --now docker >/dev/null
fi

# ---------------------------------------------------------------- files
say "Setting up $DIR"
mkdir -p "$DIR/books/Audiobooks"
for f in docker-compose.yml Caddyfile .env.example; do
  if [ -f "$HERE/$f" ] && [ "$HERE" != "$DIR" ]; then
    cp "$HERE/$f" "$DIR/$f"
  elif [ ! -f "$DIR/$f" ]; then
    curl -fsSL "https://raw.githubusercontent.com/philcharitou/kithara-server/${REF}/deploy/$f" -o "$DIR/$f"
  fi
done
# The server runs as user 1000 in the container and writes ids, uploads and merges into
# the library: the folders must be its.
chown -R 1000:1000 "$DIR/books"

if [ ! -f "$DIR/.env" ]; then
  if [ -z "$DOMAIN" ]; then
    IP="$(curl -fsS4 https://api.ipify.org || true)"
    [ -n "$IP" ] || die "Could not find this server's public address; give --domain."
    DOMAIN="${IP//./-}.sslip.io"
    echo "No --domain given: using $DOMAIN (works without DNS; a domain of your own can replace it later in $DIR/.env)."
  fi
  sed -e "s|^KITHARA_DOMAIN=.*|KITHARA_DOMAIN=${DOMAIN}|" \
      -e "s|^KITHARA_NAME=.*|KITHARA_NAME=${NAME:-Kithara}|" "$DIR/.env.example" > "$DIR/.env"
  chmod 600 "$DIR/.env"
else
  echo "Keeping the settings in $DIR/.env."
  [ -z "$DOMAIN" ] || sed -i "s|^KITHARA_DOMAIN=.*|KITHARA_DOMAIN=${DOMAIN}|" "$DIR/.env"
  [ -z "$NAME" ] || sed -i "s|^KITHARA_NAME=.*|KITHARA_NAME=${NAME}|" "$DIR/.env"
fi
DOMAIN="$(sed -n 's/^KITHARA_DOMAIN=//p' "$DIR/.env")"

# ---------------------------------------------------------------- firewall
if command -v ufw >/dev/null && ufw status | grep -q "Status: active"; then
  say "Opening ports 80 and 443 in ufw"
  ufw allow 80/tcp >/dev/null && ufw allow 443/tcp >/dev/null && ufw allow 443/udp >/dev/null
fi
# Oracle Cloud's Ubuntu images let only SSH through their own iptables rules (a REJECT
# at the end of INPUT, kept by netfilter-persistent), whatever the cloud's security list
# says: let web traffic in ahead of it, and keep that across reboots.
if command -v iptables >/dev/null && command -v netfilter-persistent >/dev/null; then
  REJECT_AT="$(iptables -L INPUT --line-numbers -n | awk '$2 == "REJECT" {print $1; exit}')"
  if [ -n "$REJECT_AT" ]; then
    say "Opening ports 80 and 443 in iptables (Oracle Cloud)"
    iptables -C INPUT -p tcp -m multiport --dports 80,443 -j ACCEPT 2>/dev/null ||
      iptables -I INPUT "$REJECT_AT" -p tcp -m multiport --dports 80,443 -j ACCEPT
    iptables -C INPUT -p udp --dport 443 -j ACCEPT 2>/dev/null ||
      iptables -I INPUT "$REJECT_AT" -p udp --dport 443 -j ACCEPT
    netfilter-persistent save >/dev/null 2>&1
  fi
fi

cd "$DIR"
PROJECT="$(basename "$DIR")"
VOLUME="${PROJECT}_kithara-data"

# ---------------------------------------------------------------- restore
if [ -n "$RESTORE" ]; then
  docker volume create "$VOLUME" >/dev/null
  if docker run --rm -v "$VOLUME":/data alpine test -f /data/users.json; then
    die "This server already has accounts; not restoring over them."
  fi
  say "Restoring $(basename "$RESTORE")"
  docker run --rm -v "$VOLUME":/data -v "$RESTORE":/backup.zip:ro alpine \
    sh -c 'cd /data && unzip -oq /backup.zip && chown -R 1000:1000 /data'
fi

# ---------------------------------------------------------------- start
say "Starting kithara-server and Caddy"
docker compose pull -q
docker compose up -d

# An account to sign in with, if there is none yet.
sleep 3
if ! docker compose exec -T kithara test -f /data/users.json 2>/dev/null; then
  if [ -t 0 ]; then
    say "Make the first account (it is the admin)"
    read -rp "User name: " ADMIN
    docker compose exec kithara kithara-server user add "$ADMIN"
  else
    echo "Make the first account with:  cd $DIR && docker compose exec kithara kithara-server user add NAME"
  fi
fi

IP="$(curl -fsS4 https://api.ipify.org 2>/dev/null || hostname -I | cut -d' ' -f1)"
say "Done"
cat <<EOF
  Address:   https://${DOMAIN}   (the certificate can take a minute the first time)
  In the app: Add a library → Kithara sync server → https://${DOMAIN}

  Copy your books in: the folders that hold your libraries (Audiobooks, Kids…) go into
  ${DIR}/books, hidden .kithara-id files included (they keep each book the same book):
    rsync -avP /path/to/books/ root@${IP}:${DIR}/books/        (Linux, macOS)
    scp -r "C:\path\to\books\Audiobooks" root@${IP}:${DIR}/books/   (Windows)
    ssh root@${IP} chown -R 1000:1000 ${DIR}/books
  then press Rescan on the web page.

  Updates:   cd ${DIR} && docker compose pull && docker compose up -d
  Logs:      cd ${DIR} && docker compose logs -f kithara
  Settings:  ${DIR}/.env   Books: ${DIR}/books   Data: the ${VOLUME} volume
  Turn on daily backups under Settings → Backups, and keep a copy off this server.
EOF
