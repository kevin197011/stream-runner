#!/usr/bin/env ruby
# frozen_string_literal: true

require 'fileutils'

APP = 'stream-runner'
DIST_DIR = 'dist'.freeze
BIN_DIR = "#{DIST_DIR}/bin".freeze
CONFIG_DIR = "#{DIST_DIR}/config".freeze
SCRIPTS_DIR = "#{DIST_DIR}/scripts".freeze

puts '[*] Cleaning previous build...'
FileUtils.rm_rf(DIST_DIR)
[BIN_DIR, CONFIG_DIR, SCRIPTS_DIR].each { |d| FileUtils.mkdir_p(d) }

# 获取项目根目录（脚本在 scripts/ 目录中）
project_root = File.expand_path('..', __dir__)

puts '[*] Writing config/streams.yml...'
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
  File.write("#{CONFIG_DIR}/streams.yml", streams_yaml)
rescue StandardError => e
  abort "ERROR: Failed to write streams.yml: #{e.message}"
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


puts '[*] Building Linux binary (amd64)...'
Dir.chdir(project_root) do
  env = { 'GOOS' => 'linux', 'GOARCH' => 'amd64' }

  # Run go mod tidy to ensure dependencies are up to date
  abort 'ERROR: go mod tidy failed' unless system(env, 'go mod tidy')

  # Build the binary
  abort 'ERROR: go build failed' unless system(env, "go build -o #{DIST_DIR}/#{APP} main.go")

  # Verify binary was created
  abort 'ERROR: Binary file was not created' unless File.exist?("#{DIST_DIR}/#{APP}")

  puts "[*] Binary built successfully: #{DIST_DIR}/#{APP}"
end

# Move binary to dist/bin for nfpm
FileUtils.mv("#{DIST_DIR}/#{APP}", "#{BIN_DIR}/#{APP}")

puts '[*] Checking nfpm installation...'
unless system('which nfpm > /dev/null 2>&1')
  abort 'ERROR: nfpm is not installed. Install it with: go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest'
end

puts '[*] Packaging with nfpm...'
Dir.chdir(project_root) do
  # Build .deb package
  puts '[*] Building .deb package...'
  unless system("nfpm pkg --packager deb --config nfpm.yaml --target dist/")
    abort 'ERROR: Failed to create .deb package'
  end

  # Build .rpm package
  puts '[*] Building .rpm package...'
  unless system("nfpm pkg --packager rpm --config nfpm.yaml --target dist/")
    abort 'ERROR: Failed to create .rpm package'
  end
end

# List generated packages
puts "\n[*] Done. Generated packages:"
Dir.glob("#{DIST_DIR}/*.{deb,rpm}").each do |pkg|
  size = File.size(pkg)
  puts "  - #{File.basename(pkg)} (#{size / 1024}KB)"
end

puts "\n[*] Package contents:"
puts "  - Binary: /usr/local/bin/#{APP}"
puts "  - Config: /etc/#{APP}/streams.yml"
puts "  - Service: /etc/systemd/system/#{APP}.service"
puts "\n[*] Installation:"
puts "  Debian/Ubuntu: sudo dpkg -i dist/#{APP}_*.deb"
puts "  RHEL/CentOS:   sudo rpm -i dist/#{APP}_*.rpm"
