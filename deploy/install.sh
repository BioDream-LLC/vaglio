#!/bin/sh
# install.sh - installs Vaglio as a service, on Linux (systemd) or macOS (launchd), in one command:
#
#   curl -fsSL https://raw.githubusercontent.com/BioDream-LLC/vaglio/main/deploy/install.sh | sudo sh -s -- -version v0.1.0
#
# It downloads the release archive for this machine, refuses it unless its SHA-256 is the one in the release's
# SHA256SUMS (and, when the GitHub CLI is installed, unless GitHub's build provenance attestation for it verifies),
# installs the program as /usr/local/bin/vaglio, and runs it as the unprivileged user "vaglio" with its data in
# /var/lib/vaglio (Linux) or /usr/local/var/vaglio (macOS), mode 700, and the plan's policies in /etc/vaglio/policies.
# Run again with a newer -version to upgrade: data and policies are kept.
#
# Options: -version vX.Y.Z (required), -archive FILE -sums FILE (install a local archive instead, for testing),
# -addr HOST:PORT (default 127.0.0.1:8740), -hostname NAME (needed off loopback), -no-start.
set -eu
repo=BioDream-LLC/vaglio
version='' archive='' sums='' addr=127.0.0.1:8740 hostname='' start=1
while [ $# -gt 0 ]; do
  case $1 in
    -version) version=$2; shift 2 ;;
    -archive) archive=$2; shift 2 ;;
    -sums) sums=$2; shift 2 ;;
    -addr) addr=$2; shift 2 ;;
    -hostname) hostname=$2; shift 2 ;;
    -no-start) start=0; shift ;;
    *) echo "install.sh: unknown option $1" >&2; exit 2 ;;
  esac
done
[ -n "$version" ] || { echo "install.sh: -version vX.Y.Z is required" >&2; exit 2; }
case $version in v[0-9]*.[0-9]*.[0-9]*) ;; *) echo "install.sh: $version is not vX.Y.Z" >&2; exit 2 ;; esac
[ "$(id -u)" = 0 ] || { echo "install.sh: run it as root (sudo)" >&2; exit 1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $(uname -m) in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo "install.sh: unsupported $(uname -m)" >&2; exit 1 ;; esac
case $os in linux|darwin) ;; *) echo "install.sh: $os is not supported here; on Windows use install.ps1" >&2; exit 1 ;; esac
name=vaglio-$version-$os-$arch
tmp=$(mktemp -d) && trap 'rm -rf "$tmp"' EXIT

if [ -z "$archive" ]; then
  base=https://github.com/$repo/releases/download/$version
  curl -fsSL "$base/$name.tar.gz" -o "$tmp/$name.tar.gz"
  curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
else
  cp "$archive" "$tmp/$name.tar.gz"
  cp "${sums:?-archive needs -sums}" "$tmp/SHA256SUMS"
fi
want=$(awk -v f="$name.tar.gz" '$2 == f {print $1}' "$tmp/SHA256SUMS")
if command -v sha256sum >/dev/null; then got=$(sha256sum "$tmp/$name.tar.gz" | cut -d' ' -f1); else got=$(shasum -a 256 "$tmp/$name.tar.gz" | cut -d' ' -f1); fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "install.sh: $name.tar.gz does not match SHA256SUMS: not installed" >&2; exit 1; }
echo "SHA-256 matches SHA256SUMS."
if [ -z "$archive" ] && command -v gh >/dev/null; then
  gh attestation verify "$tmp/$name.tar.gz" --repo "$repo" >/dev/null || { echo "install.sh: the build provenance does not verify: not installed" >&2; exit 1; }
  echo "GitHub build provenance verifies: built from $repo by its release workflow."
fi
tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
install -m 755 "$tmp/$name/vaglio" /usr/local/bin/vaglio.new && mv /usr/local/bin/vaglio.new /usr/local/bin/vaglio

mkdir -p /etc/vaglio/policies
if [ "$os" = linux ]; then data=/var/lib/vaglio; else data=/usr/local/var/vaglio; fi
flags="serve -data $data -policies /etc/vaglio/policies -addr $addr"
[ -n "$hostname" ] && flags="$flags -hostname $hostname"

if [ "$os" = linux ]; then
  id vaglio >/dev/null 2>&1 || useradd --system --home-dir "$data" --shell /usr/sbin/nologin vaglio
  mkdir -p "$data" && chown vaglio:vaglio "$data" && chmod 700 "$data"
  cat > /etc/systemd/system/vaglio.service <<EOF
[Unit]
Description=Vaglio prior authorization decisions
After=network-online.target
Wants=network-online.target

[Service]
User=vaglio
Group=vaglio
ExecStart=/usr/local/bin/vaglio $flags
Restart=on-failure
RestartSec=5
# Keep the audit log, decisions and records to this one directory, and the program from everything else.
ReadWritePaths=$data
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
NoNewPrivileges=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictNamespaces=true
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
UMask=0077

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  if [ $start = 1 ]; then systemctl enable --now vaglio; systemctl restart vaglio; fi
  logs="journalctl -u vaglio"
else
  dscl . -read /Users/_vaglio >/dev/null 2>&1 || {
    uid=$(dscl . -list /Users UniqueID | awk '$2 >= 400 && $2 < 500 {print $2}' | sort -n | tail -1); uid=$(( ${uid:-399} + 1 ))
    dscl . -create /Groups/_vaglio PrimaryGroupID "$uid"
    dscl . -create /Users/_vaglio UniqueID "$uid"
    dscl . -create /Users/_vaglio PrimaryGroupID "$uid"
    dscl . -create /Users/_vaglio UserShell /usr/bin/false
    dscl . -create /Users/_vaglio NFSHomeDirectory "$data"
  }
  mkdir -p "$data" && chown _vaglio:_vaglio "$data" && chmod 700 "$data"
  args=$(for a in /usr/local/bin/vaglio $flags; do printf '    <string>%s</string>\n' "$a"; done)
  cat > /Library/LaunchDaemons/health.biodream.vaglio.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>health.biodream.vaglio</string>
  <key>ProgramArguments</key>
  <array>
$args
  </array>
  <key>UserName</key><string>_vaglio</string>
  <key>GroupName</key><string>_vaglio</string>
  <key>Umask</key><integer>63</integer>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$data/vaglio.log</string>
  <key>StandardErrorPath</key><string>$data/vaglio.log</string>
</dict>
</plist>
EOF
  if [ $start = 1 ]; then
    launchctl bootout system/health.biodream.vaglio 2>/dev/null || true
    launchctl bootstrap system /Library/LaunchDaemons/health.biodream.vaglio.plist
  fi
  logs="cat $data/vaglio.log"
fi
/usr/local/bin/vaglio version
echo "Installed. Put the plan's policies in /etc/vaglio/policies. The first start prints the administrator's password: $logs"
echo "The console's certificate authority, to trust once, is $data/tls/ca.crt."
