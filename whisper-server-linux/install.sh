#!/usr/bin/env bash
# Installs whisper.cpp's HTTP server (GPU via Vulkan) as a systemd service for
# aglink-desktop's remote speech recognition (AGLINK_WHISPER_URL).
# Needs: cmake, g++, libvulkan-dev, glslc, spirv-headers. Run as the user that will own it;
# sudo is used only to write the unit file.
#   MODEL=small PORT=8178 ./install.sh
set -euo pipefail

REF="${WHISPER_REF:-v1.9.5}"
MODEL="${MODEL:-small}"
PORT="${PORT:-8178}"
HOST="${HOST:-0.0.0.0}"
DIR="${DIR:-$HOME/aglink-whisper}"

mkdir -p "$DIR"
cd "$DIR"
if [ ! -d src ]; then
  git clone --depth 1 --branch "$REF" https://github.com/ggml-org/whisper.cpp src
fi
cmake -S src -B build -DGGML_VULKAN=1 -DCMAKE_BUILD_TYPE=Release -DWHISPER_BUILD_TESTS=OFF
cmake --build build -j"$(nproc)" --target whisper-server

mkdir -p models
if [ ! -f "models/ggml-$MODEL.bin" ]; then
  bash src/models/download-ggml-model.sh "$MODEL" "$DIR/models"
fi

UNIT=/etc/systemd/system/aglink-whisper.service
sudo tee "$UNIT" >/dev/null <<UNIT_EOF
[Unit]
Description=aglink whisper.cpp speech recognition server
After=network-online.target

[Service]
User=$USER
WorkingDirectory=$DIR
ExecStart=$DIR/build/bin/whisper-server -m $DIR/models/ggml-$MODEL.bin -l ko -t 4 --host $HOST --port $PORT
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT_EOF
sudo systemctl daemon-reload
sudo systemctl enable --now aglink-whisper.service
echo "aglink-whisper listening on $HOST:$PORT (model $MODEL)"
