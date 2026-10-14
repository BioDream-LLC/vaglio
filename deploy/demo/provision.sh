#!/bin/sh
# provision.sh - creates or updates the public demonstration, vaglio.perfuse.health, from this machine.
#
# One Amazon Lightsail instance (nano: 512 MB, 2 vCPUs, about $5 a month with its IPv4 address), a static IP, the DNS
# record in the perfuse.health zone, and setup.sh run on it. Port 443 is open to everybody, port 80 only for Let's
# Encrypt, SSH only to the address this is run from. The binary is built here from this checkout (Vaglio has no release
# archives yet). Run again to upgrade: the certificate is kept; the invented cases are rebuilt every night anyway.
#
# Needs the AWS CLI signed in to the account that holds the perfuse.health zone. The SSH key is kept in $KEYS (default
# ~/keys), mode 600, and never printed.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
export AWS_PAGER=""
region=us-east-1
name=vaglio-demo
host=vaglio.perfuse.health
keys=${KEYS:-$HOME/keys}
key="$keys/lightsail-$region.pem"

build=$(mktemp -d)
trap 'rm -rf "$build"' EXIT
(cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -buildid= -X main.version=demo-$(git rev-parse --short HEAD)" -o "$build/vaglio" ./cmd/vaglio)
cp "$root"/policies/*.vaglio "$build/"

if ! aws lightsail get-instance --region $region --instance-name $name >/dev/null 2>&1; then
  echo "creating $name"
  aws lightsail create-instances --region $region --instance-names $name --availability-zone ${region}a \
    --blueprint-id debian_12 --bundle-id nano_3_0 --tags key=purpose,value=vaglio-public-demo >/dev/null
fi
if [ ! -s "$key" ]; then
  umask 077
  aws lightsail download-default-key-pair --region $region --query privateKeyBase64 --output text > "$key"
fi
until [ "$(aws lightsail get-instance-state --region $region --instance-name $name --query state.name --output text)" = running ]; do
  sleep 5
done
if ! aws lightsail get-static-ip --region $region --static-ip-name $name-ip >/dev/null 2>&1; then
  aws lightsail allocate-static-ip --region $region --static-ip-name $name-ip >/dev/null
fi
attached=$(aws lightsail get-static-ip --region $region --static-ip-name $name-ip --query staticIp.attachedTo --output text)
[ "$attached" = "$name" ] || aws lightsail attach-static-ip --region $region --static-ip-name $name-ip --instance-name $name >/dev/null
ip=$(aws lightsail get-static-ip --region $region --static-ip-name $name-ip --query staticIp.ipAddress --output text)

mine=$(curl -fsS https://checkip.amazonaws.com)
aws lightsail put-instance-public-ports --region $region --instance-name $name --port-infos \
  "fromPort=80,toPort=80,protocol=tcp" "fromPort=443,toPort=443,protocol=tcp" \
  "fromPort=22,toPort=22,protocol=tcp,cidrs=$mine/32" >/dev/null

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
ssh $ssh_opts admin@"$ip" "sudo sh /tmp/vaglio-demo/setup.sh"
