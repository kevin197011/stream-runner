#!/bin/bash
set -e
systemctl daemon-reload
systemctl enable stream-runner || true
echo "Stream Runner installed successfully"
echo "Edit /etc/stream-runner/streams.yml to configure streams"
echo "Then start the service: systemctl start stream-runner"

