# whisper-server-linux

Installs a GPU speech-recognition server that aglink-desktop can use instead of
running whisper on the PC's CPU.

On the Linux server (Ubuntu, NVIDIA/AMD GPU with a Vulkan driver):

    sudo apt install cmake g++ libvulkan-dev glslc glslang-tools spirv-headers git curl
    MODEL=small PORT=8178 ./install.sh

It builds whisper.cpp (Vulkan), downloads the model and registers the
`aglink-whisper` systemd service (starts at boot, restarts on failure).
`MODEL` is any whisper.cpp model name (`small`, `medium`, `large-v3-turbo-q5_0`).

On the PC, point aglink-desktop at it:

    AGLINK_WHISPER_URL=http://<server>:8178

When the server does not answer, the app falls back to the local whisper.
There is no authentication; keep the port on a trusted network.
