#!/bin/sh
# setup.sh - run on the demonstration instance as root (provision.sh copies this directory there and runs it).
# Installs the binary and the policies, a Let's Encrypt certificate for vaglio.perfuse.health, the service, the
# invented traffic and the nightly reset. Safe to run again.
set -eu
host=vaglio.perfuse.health
port=${1:-8443}
here=$(cd "$(dirname "$0")" && pwd)

apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl certbot python3 poppler-utils >/dev/null
id vaglio >/dev/null 2>&1 || useradd --system --home /opt/vaglio --shell /usr/sbin/nologin vaglio
install -d -o vaglio -g vaglio -m 0700 /opt/vaglio/data /opt/vaglio/logs
install -d -o root -g vaglio -m 0750 /opt/vaglio/conf /opt/vaglio/tls /opt/vaglio/policies

install -m 0755 "$here/vaglio" /opt/vaglio/vaglio
rm -f /opt/vaglio/policies/*.vaglio
install -o root -g vaglio -m 0640 "$here"/*.vaglio /opt/vaglio/policies/
install -o root -g vaglio -m 0640 "$here/template.json" /opt/vaglio/conf/
install -m 0755 "$here/traffic.py" /opt/vaglio/traffic.py
# The PAS bearer token: made once, readable by the service only. The traffic script, run as the service's user, sends it.
if [ ! -s /opt/vaglio/conf/token ]; then
  head -c 32 /dev/urandom | base64 | tr -dc A-Za-z0-9 > /opt/vaglio/conf/token
fi
# The appeals system's and the fax service's tokens, for the invented appeals and faxes the traffic sends.
for t in appeals-token fax-token; do
  [ -s /opt/vaglio/conf/$t ] || head -c 32 /dev/urandom | base64 | tr -dc A-Za-z0-9 > /opt/vaglio/conf/$t
done
chown root:vaglio /opt/vaglio/conf/token /opt/vaglio/conf/appeals-token /opt/vaglio/conf/fax-token
chmod 0640 /opt/vaglio/conf/token /opt/vaglio/conf/appeals-token /opt/vaglio/conf/fax-token
# The traffic goes to this machine by its public name, so the certificate matches.
grep -q " $host\$" /etc/hosts || echo "127.0.0.1 $host" >> /etc/hosts

# Let's Encrypt on port 80, which neither Vaglio nor Perfuse uses (Perfuse's own renewals use it the same way, briefly). Vaglio reloads a changed certificate by itself, so the renewal
# hook only copies it into place.
if [ ! -d "/etc/letsencrypt/live/$host" ]; then
  certbot certonly --standalone --non-interactive --agree-tos --register-unsafely-without-email -d "$host"
fi
cat > /etc/letsencrypt/renewal-hooks/deploy/vaglio.sh <<HOOK
#!/bin/sh
install -o root -g vaglio -m 0640 /etc/letsencrypt/live/$host/fullchain.pem /etc/letsencrypt/live/$host/privkey.pem /opt/vaglio/tls/
HOOK
chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/vaglio.sh
install -o root -g vaglio -m 0640 "/etc/letsencrypt/live/$host/fullchain.pem" "/etc/letsencrypt/live/$host/privkey.pem" /opt/vaglio/tls/

install -m 0644 "$here"/vaglio-*.service "$here"/vaglio-*.timer /etc/systemd/system/
sed -i "s/-addr :[0-9]*/-addr :$port/" /etc/systemd/system/vaglio-demo.service
echo "https://$host:$port" > /opt/vaglio/conf/url && chown root:vaglio /opt/vaglio/conf/url && chmod 0640 /opt/vaglio/conf/url
systemctl daemon-reload
systemctl enable --now vaglio-demo
systemctl restart vaglio-demo
systemctl enable --now vaglio-traffic.timer vaglio-reset.timer
sleep 3
systemctl start vaglio-reset.service
curl -fsS -o /dev/null -w "%{http_code}" "https://$host:$port/" && echo " - Vaglio is up on https://$host:$port"
