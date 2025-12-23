#!/bin/bash
set -e
systemctl stop stream-runner || true
systemctl disable stream-runner || true

