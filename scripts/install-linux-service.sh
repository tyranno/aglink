#!/usr/bin/env bash
# aglink systemd user 서비스 설치 스크립트 (Linux)
# 대상 머신에서 직접 실행하세요.
# 사용법: bash install-linux-service.sh [BINARY_PATH]
#   BINARY_PATH: aglink 바이너리 경로 (기본: ~/bin/aglink)
#
# 특징:
#   - systemd user 서비스 (루트 권한 불필요)
#   - 자동 재시작 on-failure, 부팅 시 자동 시작 (loginctl enable-linger)
#
# 주의 두 가지:
#   1) 바이너리를 ~/aglink 에 두지 말 것 — 그 경로는 서비스의 기본 작업 홈
#      (host/homedir.go 의 defaultHomeDir)이라, 파일이 있으면 워커의 cwd 가
#      디렉터리가 아니게 되어 claude 가 exec 단계에서 실패한다.
#   2) 이 스크립트는 ~/.aglink 를 만들지 않는다 — pre-rename ~/.teleclaude 가
#      있는 머신에서 ~/.aglink 가 먼저 존재하면 dataDir() 이 legacy 복사
#      마이그레이션을 건너뛰고 빈 디렉터리로 시작해버린다. 그래서 서비스 로그는
#      데이터 디렉터리가 아니라 ~/.local/state/aglink 에 쓴다.

set -euo pipefail

BINARY="${1:-$HOME/bin/aglink}"
SERVICE_NAME="aglink"
SERVICE_FILE="${HOME}/.config/systemd/user/${SERVICE_NAME}.service"
LOG_DIR="${HOME}/.local/state/aglink"

# 사전 검사
if [[ ! -x "${BINARY}" ]]; then
    echo "❌ 바이너리를 찾을 수 없거나 실행 권한이 없습니다: ${BINARY}"
    echo "   deploy-linux.sh 로 먼저 배포하거나 chmod +x ${BINARY} 실행"
    exit 1
fi

if [[ "$(readlink -f "${BINARY}")" == "$(readlink -f "${HOME}/aglink")" ]]; then
    echo "❌ 바이너리가 ~/aglink 에 있습니다 — 그 경로는 서비스의 작업 홈입니다."
    echo "   ~/bin/aglink 등 다른 경로로 옮긴 뒤 다시 실행하세요."
    exit 1
fi

# 설정 파일은 ~/.aglink 또는 pre-rename ~/.teleclaude 중 어디에 있어도 된다.
# (없어도 첫 실행 시 마법사가 만든다 — 여기서 디렉터리를 만들지는 않는다.)
have_config=0
for d in "${HOME}/.aglink" "${HOME}/.teleclaude"; do
    if [[ -f "${d}/config.yaml" || -f "${d}/config.txt" ]]; then
        have_config=1
        echo "ℹ  설정 발견: ${d}"
        break
    fi
done
if [[ "${have_config}" -eq 0 ]]; then
    echo "⚠  설정 파일이 없습니다 (~/.aglink, ~/.teleclaude 모두 확인)."
    echo "   서비스 등록 전에 '${BINARY} run' 으로 설정 마법사를 먼저 돌리세요."
    read -rp "   계속 진행할까요? [y/N]: " yn
    [[ "${yn,,}" == "y" ]] || exit 1
fi

# PATH에 claude CLI가 있는지 확인
if ! command -v claude >/dev/null 2>&1; then
    echo "⚠  claude CLI를 PATH에서 찾을 수 없습니다."
    echo "   NVM을 쓴다면 PATH에 node bin 경로를 추가하거나 설정에 claude.path 를 지정하세요."
fi

mkdir -p "${LOG_DIR}"
mkdir -p "$(dirname "${SERVICE_FILE}")"

# 기존 서비스 중단
if systemctl --user is-active "${SERVICE_NAME}.service" &>/dev/null; then
    echo "▶ 기존 서비스 중단 중..."
    systemctl --user stop "${SERVICE_NAME}.service"
fi

# PATH 구성: NVM 경로 포함
NVM_BIN=""
if [[ -d "${HOME}/.nvm/versions/node" ]]; then
    LATEST_NODE="$(ls -v "${HOME}/.nvm/versions/node" | tail -1)"
    if [[ -n "${LATEST_NODE}" ]]; then
        NVM_BIN=":${HOME}/.nvm/versions/node/${LATEST_NODE}/bin"
    fi
fi
SERVICE_PATH="/usr/local/bin:/usr/bin:/bin:${HOME}/.local/bin${NVM_BIN}"

cat > "${SERVICE_FILE}" << EOF
[Unit]
Description=aglink - Telegram Claude Agent
After=network.target
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=${BINARY} run
WorkingDirectory=${HOME}
Restart=on-failure
RestartSec=5
KillMode=process
Environment=HOME=${HOME}
Environment=PATH=${SERVICE_PATH}
StandardOutput=append:${LOG_DIR}/aglink.log
StandardError=append:${LOG_DIR}/aglink.error.log

[Install]
WantedBy=default.target
EOF

echo "▶ 서비스 파일 생성: ${SERVICE_FILE}"

# linger 활성화 (로그아웃 후에도 서비스 유지)
if loginctl enable-linger "${USER}" 2>/dev/null; then
    echo "▶ loginctl enable-linger: 로그아웃 후에도 서비스 유지 활성화"
fi

systemctl --user daemon-reload
systemctl --user enable "${SERVICE_NAME}.service"
systemctl --user start "${SERVICE_NAME}.service"

sleep 2
echo ""
systemctl --user status "${SERVICE_NAME}.service" --no-pager

echo ""
echo "✅ aglink 서비스 설치 완료"
echo "   로그:    tail -f ${LOG_DIR}/aglink.error.log"
echo "   중단:    systemctl --user stop aglink"
echo "   재시작:  systemctl --user restart aglink"
echo "   제거:    systemctl --user disable --now aglink && rm ${SERVICE_FILE}"
