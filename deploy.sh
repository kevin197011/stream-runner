#!/usr/bin/env bash
# Copyright (c) 2026 kk
#
# This software is released under the MIT License.
# https://opensource.org/licenses/MIT

set -o errexit
set -o nounset
set -o pipefail

# curl exec:
#   curl -fsSL https://raw.githubusercontent.com/kevin197011/stream-runner/main/deploy.sh | sudo bash
#   curl -fsSL https://raw.githubusercontent.com/kevin197011/stream-runner/main/deploy.sh | sudo bash -s -- --uninstall
#
# Env:
#   SR_VERSION  tag or bare version (v1.0.0 / 1.0.0); default: latest GitHub release
#   SR_REPO     owner/name (default: kevin197011/stream-runner)
#   SR_ACTION   install (default) | uninstall

SR_REPO="${SR_REPO:-kevin197011/stream-runner}"
SR_CURL_UA='Mozilla/5.0 (compatible; stream-runner-deploy/1.0)'
SR_ACTION="${SR_ACTION:-install}"

BIN=/usr/local/bin/stream-runner
UNIT=/etc/systemd/system/stream-runner.service
CFG_DIR=/etc/stream-runner
CFG="${CFG_DIR}/streams.yml"
SHARE_CFG=/usr/share/stream-runner/streams.yml

sr::log() { printf '%s\n' "$*"; }
sr::die() {
	printf 'ERROR: %s\n' "$*" >&2
	exit 1
}

sr::curl() {
	curl -fsSL -A "$SR_CURL_UA" "$@"
}

sr::need_root() {
	if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
		sr::die "run as root (sudo). Example: curl -fsSL .../deploy.sh | sudo bash"
	fi
}

sr::arch() {
	case "$(uname -m 2>/dev/null || echo unknown)" in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	*) sr::die "unsupported arch: $(uname -m) (need amd64 or arm64)" ;;
	esac
}

sr::resolve_version() {
	if [[ -n "${SR_VERSION:-}" ]]; then
		local v="$SR_VERSION"
		[[ "$v" != v* ]] && v="v${v}"
		echo "$v"
		return
	fi
	local tag
	tag="$(sr::curl "https://api.github.com/repos/${SR_REPO}/releases/latest" |
		grep -m1 '"tag_name"' |
		sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')" || true
	[[ -n "$tag" ]] || sr::die "could not resolve latest release; set SR_VERSION=vX.Y.Z"
	echo "$tag"
}

sr::ensure_ffmpeg() {
	if command -v ffmpeg >/dev/null 2>&1; then
		return 0
	fi
	sr::log "ffmpeg missing — installing..."
	if command -v apt-get >/dev/null 2>&1; then
		apt-get update -y >/dev/null
		apt-get install -y ffmpeg || sr::die "failed to install ffmpeg (apt)"
	elif command -v dnf >/dev/null 2>&1; then
		dnf install -y ffmpeg || sr::die "failed to install ffmpeg (dnf)"
	elif command -v yum >/dev/null 2>&1; then
		yum install -y ffmpeg || sr::die "failed to install ffmpeg (yum)"
	else
		sr::die "ffmpeg required but no apt/dnf/yum available"
	fi
	command -v ffmpeg >/dev/null 2>&1 || sr::die "ffmpeg still missing after install"
	sr::log "ffmpeg installed: $(command -v ffmpeg)"
}

# Former packaging/postinstall: config template + enable/restart unit.
sr::setup_service() {
	install -d -m 0755 "$CFG_DIR"
	if [[ ! -e "$CFG" && -e "${CFG}.rpmsave" ]]; then
		mv "${CFG}.rpmsave" "$CFG"
	fi
	if [[ ! -e "$CFG" && -e "$SHARE_CFG" ]]; then
		install -m 0644 "$SHARE_CFG" "$CFG"
	fi
	systemctl daemon-reload || true
	systemctl enable stream-runner || true
	systemctl restart stream-runner || true
	sr::log "service enabled and started"
	sr::log "Edit ${CFG} then: systemctl reload stream-runner"
}

sr::try_package() {
	local arch="$1" tag="$2" ver="${2#v}"
	local base="https://github.com/${SR_REPO}/releases/download/${tag}"
	local tmp
	tmp="$(mktemp -d)"
	# shellcheck disable=SC2064
	trap "rm -rf '$tmp'" RETURN

	if command -v apt-get >/dev/null 2>&1; then
		local deb="stream-runner_${ver}_${arch}.deb"
		if sr::curl -o "${tmp}/${deb}" "${base}/${deb}" 2>/dev/null; then
			# apt resolves Depends (ffmpeg); fall back to dpkg + fix-broken.
			if apt-get install -y "${tmp}/${deb}" 2>/dev/null ||
				{ dpkg -i "${tmp}/${deb}" 2>/dev/null && apt-get install -y -f; }; then
				sr::log "installed via ${deb}"
				return 0
			fi
		fi
	fi
	if command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1; then
		local rpm="stream-runner_${ver}_${arch}.rpm"
		if sr::curl -o "${tmp}/${rpm}" "${base}/${rpm}" 2>/dev/null; then
			if command -v dnf >/dev/null 2>&1 && dnf install -y "${tmp}/${rpm}" 2>/dev/null; then
				sr::log "installed via ${rpm}"
				return 0
			fi
			if command -v yum >/dev/null 2>&1 && yum install -y "${tmp}/${rpm}" 2>/dev/null; then
				sr::log "installed via ${rpm}"
				return 0
			fi
			if rpm -Uvh "${tmp}/${rpm}" 2>/dev/null; then
				sr::log "installed via ${rpm} (rpm -Uvh; install ffmpeg separately if needed)"
				return 0
			fi
		fi
	fi
	return 1
}

sr::install_from_tarball() {
	local arch="$1" tag="$2" ver="${2#v}"
	local archive="stream-runner_${ver}_linux_${arch}.tar.gz"
	local url="https://github.com/${SR_REPO}/releases/download/${tag}/${archive}"
	local tmp
	tmp="$(mktemp -d)"
	# shellcheck disable=SC2064
	trap "rm -rf '$tmp'" RETURN

	sr::log "Downloading ${archive}..."
	sr::curl -o "${tmp}/${archive}" "$url" || sr::die "download failed: ${url}"
	tar -xzf "${tmp}/${archive}" -C "$tmp" || sr::die "extract failed"

	local bin unit cfg
	bin="$(find "$tmp" -type f -name stream-runner | head -1 || true)"
	unit="$(find "$tmp" -type f -name stream-runner.service | head -1 || true)"
	cfg="$(find "$tmp" -type f -name streams.yml | head -1 || true)"
	[[ -n "$bin" && -f "$bin" ]] || sr::die "archive missing stream-runner binary"
	[[ -n "$unit" && -f "$unit" ]] || sr::die "archive missing stream-runner.service"

	install -d -m 0755 /usr/local/bin /usr/share/stream-runner "$CFG_DIR"
	install -m 0755 "$bin" "$BIN"
	install -m 0644 "$unit" "$UNIT"
	if [[ -n "$cfg" ]]; then
		install -m 0644 "$cfg" "$SHARE_CFG"
	fi
	sr::log "installed ${BIN} (${tag}, linux/${arch})"
}

sr::install() {
	sr::need_root
	command -v curl >/dev/null 2>&1 || sr::die "curl is required"
	[[ "$(uname -s 2>/dev/null || echo unknown)" == Linux ]] ||
		sr::die "stream-runner packages target Linux only"

	local arch tag
	arch="$(sr::arch)"
	tag="$(sr::resolve_version)"
	sr::log "Installing stream-runner ${tag} (linux/${arch})"
	sr::ensure_ffmpeg

	if sr::try_package "$arch" "$tag"; then
		sr::setup_service
		return 0
	fi

	command -v tar >/dev/null 2>&1 || sr::die "tar is required for tarball fallback"
	sr::install_from_tarball "$arch" "$tag"
	sr::setup_service
}

sr::uninstall() {
	sr::need_root
	local removed=0

	# Former packaging/preremove: stop before removing files/packages.
	if systemctl list-unit-files stream-runner.service >/dev/null 2>&1 ||
		[[ -f "$UNIT" ]]; then
		systemctl disable --now stream-runner 2>/dev/null || true
	fi

	if command -v dpkg >/dev/null 2>&1 && dpkg -l stream-runner >/dev/null 2>&1; then
		dpkg -r stream-runner 2>/dev/null || true
		sr::log "removed deb package stream-runner"
		removed=1
	fi
	if command -v rpm >/dev/null 2>&1 && rpm -q stream-runner >/dev/null 2>&1; then
		rpm -e stream-runner 2>/dev/null || true
		sr::log "removed rpm package stream-runner"
		removed=1
	fi
	if [[ -e "$BIN" ]]; then
		rm -f "$BIN"
		sr::log "removed ${BIN}"
		removed=1
	fi
	if [[ -e "$UNIT" ]]; then
		rm -f "$UNIT"
		systemctl daemon-reload || true
		sr::log "removed ${UNIT}"
		removed=1
	fi
	# Keep ${CFG} — operator config. Share template can go.
	if [[ -e "$SHARE_CFG" ]]; then
		rm -f "$SHARE_CFG"
		rmdir /usr/share/stream-runner 2>/dev/null || true
		removed=1
	fi

	if [[ "$removed" -eq 0 ]]; then
		sr::log "stream-runner not found"
	else
		sr::log "uninstall done (config left at ${CFG} if present)"
	fi
}

sr::parse_args() {
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--uninstall | -u | uninstall) SR_ACTION=uninstall ;;
		--install | install) SR_ACTION=install ;;
		-h | --help)
			cat <<'EOF'
Usage: deploy.sh [--install|--uninstall]
  --install     install / upgrade from GitHub Release (default)
  --uninstall   stop service and remove package/binary/unit
Env: SR_VERSION SR_REPO SR_ACTION
EOF
			exit 0
			;;
		*) sr::die "unknown arg: $1 (try --help)" ;;
		esac
		shift
	done
}

sr::parse_args "$@"
case "$SR_ACTION" in
uninstall) sr::uninstall ;;
install) sr::install ;;
*) sr::die "unknown SR_ACTION=$SR_ACTION (install|uninstall)" ;;
esac
