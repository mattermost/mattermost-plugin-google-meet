#!/bin/sh
# Creates a throwaway CA and a certificate for the Google hosts the fake answers for, then a CA
# bundle (system roots plus the throwaway CA) for Mattermost's SSL_CERT_FILE. Runs in the `certs`
# service of e2e/playwright/docker-compose.yml. Copied from mattermost-plugin-google-drive
# e2e/playwright/fake-google/gen-certs.sh.
set -eu

# FAKE_GOOGLE_HOSTS is set in e2e/playwright/docker-compose.yml, which gives the fake the same list.
HOSTS="$FAKE_GOOGLE_HOSTS"

cd /certs

# Reuse the certificates (the volume outlives `docker compose up`) only while they cover the same
# hosts, are valid for at least another day, and the leaf was signed by the CA in the bundle.
# `hosts` is written last, so an interrupted run never counts as complete.
if [ -f fake.crt ] && [ -f fake.key ] && [ -f ca.crt ] && [ -f ca-bundle.crt ] && [ "$(cat hosts 2>/dev/null)" = "$HOSTS" ] \
  && openssl x509 -checkend 86400 -noout -in fake.crt >/dev/null \
  && openssl x509 -checkend 86400 -noout -in ca.crt >/dev/null \
  && openssl verify -CAfile ca.crt fake.crt >/dev/null 2>&1; then
  exit 0
fi
rm -f hosts

openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -keyout ca.key -out ca.crt \
  -subj "/CN=Fake Google E2E CA" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign"

SAN=""
for host in $HOSTS; do
  SAN="${SAN:+$SAN,}DNS:$host"
done
openssl req -newkey rsa:2048 -nodes -keyout fake.key -out fake.csr -subj "/CN=meet.googleapis.com"
printf "subjectAltName=%s\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n" "$SAN" > san.ext
openssl x509 -req -in fake.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -extfile san.ext -out fake.crt

cat /etc/ssl/certs/ca-certificates.crt ca.crt > ca-bundle.crt
printf "%s" "$HOSTS" > hosts
# Certificates and the bundle are read by Mattermost; the keys only by the fake, which runs as root.
chmod 644 ./*.crt hosts
chmod 600 ./*.key
