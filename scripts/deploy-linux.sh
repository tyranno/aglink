#!/usr/bin/env bash
# aglink Linux 배포 스크립트 (host 모듈만 — 헤드리스 서버용)
# 사용법: ./scripts/deploy-linux.sh [SSH_HOST] [REMOTE_PATH]
#   SSH_HOST   : 배포 대상 SSH alias 또는 user@host (기본: nanopi)
#   REMOTE_PATH: 바이너리 설치 경로 (기본: ~/bin/aglink)
#
# screen/web/chat 모듈은 배포하지 않는다 — 헤드리스 리눅스에서는 쓰이지 않고,
# 없으면 host 가 조용히 건너뛴다.
#
# 사전 준비:
#   - SSH key 인증 설정 완료
#   - GOARCH 를 대상 아키텍처에 맞게 (ARM64: arm64, x86-64: amd64)
#
# 예시:
#   ./scripts/deploy-linux.sh                          # nanopi (ARM64)
#   ./scripts/deploy-linux.sh user@192.168.1.100       # IP 직접 지정
#   GOARCH=amd64 ./scripts/deploy-linux.sh myserver    # x86-64 서버

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

SSH_HOST="${1:-nanopi}"
# NOT ~/aglink: 그 경로는 서비스의 기본 작업 홈이다 (host/homedir.go 의
# defaultHomeDir). 거기에 바이너리를 두면 모든 워커의 cwd 가 파일이 되어
# claude 가 "fork/exec ...: not a directory" 로 시작조차 못 한다.
REMOTE_PATH="${2:-~/bin/aglink}"
GOARCH="${GOARCH:-arm64}"
BINARY="${REPO_ROOT}/aglink-linux-${GOARCH}"

echo "▶ 빌드: linux/${GOARCH} → $(basename "${BINARY}")"
# go.work 아래에서는 리포 전역 -mod=mod 설정이 충돌하므로 readonly 로 덮어쓴다.
(cd "${REPO_ROOT}/host" && GOFLAGS=-mod=readonly GOOS=linux GOARCH="${GOARCH}" go build -o "${BINARY}" .)

echo "▶ 배포: $(basename "${BINARY}") → ${SSH_HOST}:${REMOTE_PATH}"
ssh "${SSH_HOST}" "mkdir -p \$(dirname ${REMOTE_PATH})"
scp "${BINARY}" "${SSH_HOST}:${REMOTE_PATH}"
ssh "${SSH_HOST}" "chmod +x ${REMOTE_PATH}"

# 서비스가 등록되어 있으면 재시작
if ssh "${SSH_HOST}" "systemctl --user is-enabled aglink.service &>/dev/null"; then
    echo "▶ 서비스 재시작: aglink.service"
    ssh "${SSH_HOST}" "systemctl --user restart aglink.service && systemctl --user status aglink.service --no-pager -l"
else
    echo "ℹ  서비스 미등록 — install-linux-service.sh 를 실행하세요"
    echo "   ssh ${SSH_HOST} '${REMOTE_PATH} run'"
fi

echo "✅ 배포 완료"
