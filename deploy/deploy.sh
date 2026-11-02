#!/usr/bin/env bash
# Sets up kithara-server on Ubuntu (22.04, 24.04) or Debian (12). Run it as root:
#
#   curl -fsSLO https://raw.githubusercontent.com/philcharitou/kithara-server/main/deploy/deploy.sh
#   sudo bash deploy.sh --domain books.example.com      a server on the internet
#   sudo bash deploy.sh --home                          a machine at home, through Tailscale
#
# On the internet, Caddy serves it over HTTPS on ports 80 and 443. At home (--home),
# nothing is opened to the internet: Tailscale puts it on your own private network, with
# an HTTPS address like https://basement.your-tailnet.ts.net that your devices reach from
# anywhere once they are signed in to Tailscale.
#
# Options:
#   --home            at home, through Tailscale (installs it, and keeps a laptop
#                     running with its lid shut)
#   --domain NAME     on the internet: the address to serve (point its DNS A record here
#                     first). Without one, <this-server's-ip>.sslip.io is used.
#   --name NAME       the server's name in the apps (default: Kithara)
#   --restore FILE    a backup zip from Settings → Backups on another kithara-server:
#                     its accounts, progress, transcripts and settings come along
#   --dir DIR         where it lives (default: /opt/kithara)
#
# Run it again any time: it keeps what is there and only fills in what is missing.
set -euo pipefail

DIR=/opt/kithara
MODE="" # public or home
DOMAIN=""
NAME=""
RESTORE=""
REF="${KITHARA_REF:-main}" # where the compose file and Caddyfile come from, if not beside this script

while [ $# -gt 0 ]; do
  case "$1" in
    --home) MODE=home; shift ;;
    --domain) DOMAIN="$2"; MODE=public; shift 2 ;;
    --name) NAME="$2"; shift 2 ;;
    --restore) RESTORE="$2"; shift 2 ;;
    --dir) DIR="$2"; shift 2 ;;
    -h|--help) sed -n '2,23p' "$0"; exit 0 ;;
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

# Which kind of set-up: as asked, else as it was set up before, else on the internet.
if [ -z "$MODE" ]; then
  MODE=public
  if [ -f "$DIR/.env" ] && grep -q '^COMPOSE_PROFILES=$' "$DIR/.env"; then
    MODE=home
  fi
fi

# ---------------------------------------------------------------- Tailscale (home)
if [ "$MODE" = home ]; then
  if ! command -v tailscale >/dev/null; then
    say "Installing Tailscale (from Tailscale's own apt repository)"
    . /etc/os-release
    apt-get install -y -qq curl >/dev/null
    curl -fsSL "https://pkgs.tailscale.com/stable/${ID}/${VERSION_CODENAME}.noarmor.gpg" -o /usr/share/keyrings/tailscale-archive-keyring.gpg
    curl -fsSL "https://pkgs.tailscale.com/stable/${ID}/${VERSION_CODENAME}.tailscale-keyring.list" -o /etc/apt/sources.list.d/tailscale.list
    apt-get update -qq
    apt-get install -y -qq tailscale >/dev/null
  fi
  if ! tailscale status >/dev/null 2>&1; then
    say "Sign this machine in to Tailscale: open the link below (make a free account if you have none)"
    tailscale up
  fi
  # A laptop in a cupboard runs with its lid shut. (From the next restart.)
  if ls /sys/class/power_supply/BAT* >/dev/null 2>&1 && [ ! -f /etc/systemd/logind.conf.d/kithara.conf ]; then
    mkdir -p /etc/systemd/logind.conf.d
    printf '[Login]\nHandleLidSwitch=ignore\nHandleLidSwitchExternalPower=ignore\nHandleLidSwitchDocked=ignore\n' \
      > /etc/systemd/logind.conf.d/kithara.conf
    echo "This is a laptop: closing the lid will no longer suspend it (from the next restart)."
  fi
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
  if [ "$MODE" = home ]; then
    DOMAIN="(Tailscale)"
  elif [ -z "$DOMAIN" ]; then
    IP="$(curl -fsS4 https://api.ipify.org || true)"
    [ -n "$IP" ] || die "Could not find this server's public address; give --domain."
    DOMAIN="${IP//./-}.sslip.io"
    echo "No --domain given: using $DOMAIN (works without DNS; a domain of your own can replace it later in $DIR/.env)."
  fi
  PROFILES=public
  [ "$MODE" = home ] && PROFILES=""
  sed -e "s|^KITHARA_DOMAIN=.*|KITHARA_DOMAIN=${DOMAIN}|" \
      -e "s|^COMPOSE_PROFILES=.*|COMPOSE_PROFILES=${PROFILES}|" \
      -e "s|^KITHARA_NAME=.*|KITHARA_NAME=${NAME:-Kithara}|" "$DIR/.env.example" > "$DIR/.env"
  chmod 600 "$DIR/.env"
else
  echo "Keeping the settings in $DIR/.env."
  [ -z "$DOMAIN" ] || sed -i "s|^KITHARA_DOMAIN=.*|KITHARA_DOMAIN=${DOMAIN}|" "$DIR/.env"
  [ -z "$NAME" ] || sed -i "s|^KITHARA_NAME=.*|KITHARA_NAME=${NAME}|" "$DIR/.env"
  if [ "$MODE" = home ]; then
    sed -i "s|^COMPOSE_PROFILES=.*|COMPOSE_PROFILES=|" "$DIR/.env"
  else
    sed -i "s|^COMPOSE_PROFILES=.*|COMPOSE_PROFILES=public|" "$DIR/.env"
  fi
fi
DOMAIN="$(sed -n 's/^KITHARA_DOMAIN=//p' "$DIR/.env")"

# ---------------------------------------------------------------- firewall (public)
if [ "$MODE" = public ] && command -v ufw >/dev/null && ufw status | grep -q "Status: active"; then
  say "Opening ports 80 and 443 in ufw"
  ufw allow 80/tcp >/dev/null && ufw allow 443/tcp >/dev/null && ufw allow 443/udp >/dev/null
fi
# Oracle Cloud's Ubuntu images let only SSH through their own iptables rules (a REJECT
# at the end of INPUT, kept by netfilter-persistent), whatever the cloud's security list
# says: let web traffic in ahead of it, and keep that across reboots.
if [ "$MODE" = public ] && command -v iptables >/dev/null && command -v netfilter-persistent >/dev/null; then
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
if [ "$MODE" = home ]; then say "Starting kithara-server"; else say "Starting kithara-server and Caddy"; fi
docker compose pull -q
docker compose up -d --remove-orphans
# Moved home from the internet: Caddy is no longer wanted.
[ "$MODE" = public ] || docker compose --profile public rm -sf caddy >/dev/null 2>&1 || true

# At home: Tailscale passes HTTPS on its own address to the server on this machine.
if [ "$MODE" = home ]; then
  say "Serving it on your Tailscale network (if asked, turn on HTTPS for your tailnet at the link)"
  tailscale serve --bg --https=443 http://127.0.0.1:8080
  DOMAIN="$(tailscale status --json --peers=false | sed -n 's/^[[:space:]]*"DNSName": "\(.*\)\.",*$/\1/p' | head -1)"
  sed -i "s|^KITHARA_DOMAIN=.*|KITHARA_DOMAIN=${DOMAIN}|" "$DIR/.env"
fi

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

if [ "$MODE" = home ]; then
  IP="$(hostname -I | cut -d' ' -f1)"
else
  IP="$(curl -fsS4 https://api.ipify.org 2>/dev/null || hostname -I | cut -d' ' -f1)"
fi
# Copied in as root where it can sign in over SSH (a cloud server), else as the user who
# ran sudo (a machine at home), by way of their home folder.
WHO="${SUDO_USER:-root}"
[ "$MODE" = public ] && WHO=root
if [ "$WHO" = root ]; then
  TO="${DIR}/books/"
  AFTER="ssh root@${IP} chown -R 1000:1000 ${DIR}/books"
else
  TO="~/books/"
  AFTER="then on this machine:  sudo cp -a ~/books/. ${DIR}/books/ && sudo chown -R 1000:1000 ${DIR}/books"
fi
say "Done"
cat <<EOF
  Address:   https://${DOMAIN}   (the certificate can take a minute the first time)
  In the app: Add a library → Kithara sync server → https://${DOMAIN}
EOF
if [ "$MODE" = home ]; then
  cat <<EOF
  Only devices signed in to your Tailscale network reach it: install Tailscale on your
  phone and sign in with the same account. Nothing is open to the internet.
EOF
fi
cat <<EOF

  Copy your books in: the folders that hold your libraries (Audiobooks, Kids…) go into
  ${DIR}/books, hidden .kithara-id files included (they keep each book the same book):
    rsync -avP /path/to/books/ ${WHO}@${IP}:${TO}        (Linux, macOS)
    scp -r "C:\path\to\books\Audiobooks" ${WHO}@${IP}:${TO}   (Windows)
    ${AFTER}
  then press Rescan on the web page.

  Updates:   cd ${DIR} && docker compose pull && docker compose up -d
  Logs:      cd ${DIR} && docker compose logs -f kithara
  Settings:  ${DIR}/.env   Books: ${DIR}/books   Data: the ${VOLUME} volume
  Turn on daily backups under Settings → Backups, and keep a copy off this server.
EOF
