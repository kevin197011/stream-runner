#!/bin/bash
set -e
install -d -m 0755 /etc/stream-runner
# An upgrade that stops shipping this path renames a modified file to .rpmsave.
if [ ! -e /etc/stream-runner/streams.yml ] && [ -e /etc/stream-runner/streams.yml.rpmsave ]; then
  mv /etc/stream-runner/streams.yml.rpmsave /etc/stream-runner/streams.yml
fi
if [ ! -e /etc/stream-runner/streams.yml ]; then
  install -m 0644 /usr/share/stream-runner/streams.yml /etc/stream-runner/streams.yml
fi
systemctl daemon-reload
systemctl enable stream-runner || true
echo "Stream Runner installed successfully"
echo "Edit /etc/stream-runner/streams.yml to configure streams"
echo "Then start the service: systemctl start stream-runner"

