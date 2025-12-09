#!/usr/bin/env ruby
# frozen_string_literal: true

require 'fileutils'

APP = 'stream-runner'
PKG_DIR = "#{APP}-pkg".freeze
BIN_DIR = "#{PKG_DIR}/bin".freeze
CONFIG_DIR = "#{PKG_DIR}/config".freeze
SCRIPTS_DIR = "#{PKG_DIR}/scripts".freeze

puts '[*] Cleaning previous package...'
FileUtils.rm_rf(PKG_DIR)
[BIN_DIR, CONFIG_DIR, SCRIPTS_DIR].each { |d| FileUtils.mkdir_p(d) }

puts '[*] Copying source files...'
begin
  FileUtils.cp('main.go', "#{PKG_DIR}/main.go")
  FileUtils.cp('go.mod', "#{PKG_DIR}/go.mod")
  FileUtils.cp('go.sum', "#{PKG_DIR}/go.sum") if File.exist?('go.sum')
rescue StandardError => e
  abort "ERROR: Failed to copy source files: #{e.message}"
end

puts '[*] Writing config/streams.yaml...'
streams_yaml = <<~YAML
  streams:
  - id: stream-1
    src: rtmp://example.com/live/stream1
    dst: rtmp://127.0.0.1:1936/live/stream1
  - id: stream-2
    src: rtmp://example.com/live/stream2
    dst: rtmp://127.0.0.1:1936/live/stream2
  - id: stream-3
    src: rtmp://example.com/live/stream3
    dst: rtmp://127.0.0.1:1936/live/stream3
  - id: stream-4
    src: rtmp://example.com/live/stream4
    dst: rtmp://127.0.0.1:1936/live/stream4
YAML
begin
  File.write("#{CONFIG_DIR}/streams.yaml", streams_yaml)
rescue StandardError => e
  abort "ERROR: Failed to write streams.yaml: #{e.message}"
end

puts '[*] Writing systemd service...'
service_file = <<~SERVICE
  [Unit]
  Description=RTMP Stream Runner
  After=network.target

  [Service]
  Type=simple
  ExecStart=/usr/local/bin/stream-runner
  Restart=always
  RestartSec=5
  PIDFile=/var/run/stream-runner.pid
  User=root
  Group=root

  [Install]
  WantedBy=multi-user.target
SERVICE
begin
  File.write("#{SCRIPTS_DIR}/#{APP}.service", service_file)
rescue StandardError => e
  abort "ERROR: Failed to write service file: #{e.message}"
end

puts '[*] Writing deploy.sh...'
deploy_sh = <<~SH
  #!/bin/bash
  set -e

  echo "[*] Stopping old service..."
  sudo systemctl stop #{APP} 2>/dev/null || true

  echo "[*] Deploying #{APP}..."
  sudo cp bin/#{APP} /usr/local/bin/
  sudo mkdir -p /etc/#{APP}
  sudo cp config/streams.yaml /etc/#{APP}/
  sudo cp scripts/#{APP}.service /etc/systemd/system/
  sudo systemctl daemon-reload
  sudo systemctl enable #{APP}
  sudo systemctl start #{APP}

  echo "[*] Waiting for service to start..."
  sleep 3

  echo "[*] Testing API endpoint..."
  if curl -s http://localhost:1985/api/v1/streams/ | jq . > /dev/null 2>&1; then
    echo "[*] API test successful"
    curl -s http://localhost:1985/api/v1/streams/ | jq .
  else
    echo "[!] Warning: API test failed or endpoint not available"
    echo "[*] Service status:"
    sudo systemctl status #{APP} --no-pager -l || true
  fi

  echo "[*] Deployment completed. Logs: /var/log/#{APP}/stream.log"
SH
begin
  File.write("#{PKG_DIR}/deploy.sh", deploy_sh)
  FileUtils.chmod('+x', "#{PKG_DIR}/deploy.sh")
rescue StandardError => e
  abort "ERROR: Failed to write deploy.sh: #{e.message}"
end

puts '[*] Building Linux binary (amd64)...'
Dir.chdir(PKG_DIR) do
  env = { 'GOOS' => 'linux', 'GOARCH' => 'amd64' }

  # Run go mod tidy to ensure dependencies are up to date
  abort 'ERROR: go mod tidy failed' unless system(env, 'go mod tidy')

  # Build the binary
  FileUtils.mkdir_p('bin')
  abort 'ERROR: go build failed' unless system(env, "go build -o bin/#{APP} main.go")

  # Verify binary was created
  abort 'ERROR: Binary file was not created' unless File.exist?("bin/#{APP}")

  puts "[*] Binary built successfully: bin/#{APP}"
end

puts '[*] Packaging tar.gz...'
# Package with directory name so files are extracted into a single directory
abort 'ERROR: Failed to create tar.gz package' unless system("tar czvf #{APP}.tar.gz -C . #{PKG_DIR}")

# Verify package was created
abort 'ERROR: Package file was not created' unless File.exist?("#{APP}.tar.gz")

package_size = File.size("#{APP}.tar.gz")
puts "[*] Package created: #{APP}.tar.gz (#{package_size / 1024}KB)"

puts "[*] Done. Generated #{APP}.tar.gz"
puts '[*] Package contents:'
puts "  - bin/#{APP} (Linux binary)"
puts '  - config/streams.yaml (configuration)'
puts "  - scripts/#{APP}.service (systemd service)"
puts '  - deploy.sh (deployment script)'
