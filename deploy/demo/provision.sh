#!/bin/sh
# provision.sh - installs or updates the public demonstration, https://vaglio.perfuse.health:8443, from this machine.
#
# It runs beside Perfuse's public test instance on the same Amazon Lightsail nano (no second instance, nothing more to
# pay): the DNS record in the perfuse.health zone, port 8443 opened, and setup.sh run there. The binary is built here
# from this checkout, or with VERSION=vX.Y.Z is that release's own archive, checked against its SHA256SUMS. Run
# again to upgrade: the certificate is kept; the invented cases are rebuilt every night anyway.
#
# Needs the AWS CLI signed in to the account that holds the perfuse.health zone. The SSH key is kept in $KEYS (default
# ~/keys), mode 600, and never printed.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
export AWS_PAGER=""
region=us-east-1
name=perfuse-test
port=8443
host=vaglio.perfuse.health
keys=${KEYS:-$HOME/keys}
key="$keys/lightsail-$region.pem"

build=$(mktemp -d)
trap 'rm -rf "$build"' EXIT
if [ -n "${VERSION:-}" ]; then
  # A published release, as anyone would install it: the archive checked against the release's SHA256SUMS.
  rel="https://github.com/BioDream-LLC/vaglio/releases/download/$VERSION"
  archive="vaglio-$VERSION-linux-amd64.tar.gz"
  curl -fsSL -o "$build/$archive" "$rel/$archive"
  curl -fsSL -o "$build/SHA256SUMS" "$rel/SHA256SUMS"
  (cd "$build" && grep " $archive\$" SHA256SUMS | { if command -v sha256sum >/dev/null; then sha256sum -c -; else shasum -a 256 -c -; fi; }) ||
    { echo "$archive does not match the release's SHA256SUMS" >&2; exit 1; }
  tar -xzf "$build/$archive" -C "$build"
  mv "$build/vaglio-$VERSION-linux-amd64/vaglio" "$build/vaglio"
  rm -rf "$build/$archive" "$build/SHA256SUMS" "$build/vaglio-$VERSION-linux-amd64"
  echo "installing the release $VERSION"
else
  (cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -buildid= -X main.version=demo-$(git rev-parse --short HEAD)" -o "$build/vaglio" ./cmd/vaglio)
fi
cp "$root"/policies/*.vaglio "$build/"

# The demonstration shares the Perfuse test instance (perfuse-test, made by Perfuse's deploy/test-instance/provision.sh)
# and its static IP. Vaglio listens on 8443; Perfuse keeps 443. Opening 8443 adds one rule and leaves the others; running
# Perfuse's provision.sh again resets the instance's ports to its own list, so run this again after it.
if ! aws lightsail get-instance --region $region --instance-name $name >/dev/null 2>&1; then
  echo "$name does not exist: create it with Perfuse's deploy/test-instance/provision.sh first" >&2
  exit 1
fi
ip=$(aws lightsail get-static-ip --region $region --static-ip-name $name-ip --query staticIp.ipAddress --output text)
aws lightsail open-instance-public-ports --region $region --instance-name $name \
  --port-info "fromPort=$port,toPort=$port,protocol=tcp" >/dev/null

zone=$(aws route53 list-hosted-zones-by-name --dns-name perfuse.health --query 'HostedZones[0].Id' --output text)
aws route53 change-resource-record-sets --hosted-zone-id "$zone" --change-batch "{\"Changes\":[{\"Action\":\"UPSERT\",
  \"ResourceRecordSet\":{\"Name\":\"$host\",\"Type\":\"A\",\"TTL\":300,\"ResourceRecords\":[{\"Value\":\"$ip\"}]}}]}" >/dev/null
until [ "$(dig +short $host @8.8.8.8 | tail -1)" = "$ip" ]; do sleep 10; done
echo "$host is $ip"

ssh_opts="-i $key -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$keys/known_hosts -o ConnectTimeout=10"
until ssh $ssh_opts admin@"$ip" true 2>/dev/null; do sleep 5; done
ssh $ssh_opts admin@"$ip" 'rm -rf /tmp/vaglio-demo && mkdir -p /tmp/vaglio-demo'
scp -q $ssh_opts "$build"/* "$here"/setup.sh "$here"/traffic.py "$here"/template.json "$here"/vaglio-*.service \
  "$here"/vaglio-*.timer admin@"$ip":/tmp/vaglio-demo/
ssh $ssh_opts admin@"$ip" "sudo sh /tmp/vaglio-demo/setup.sh $port"
