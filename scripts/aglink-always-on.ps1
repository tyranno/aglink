<#
.SYNOPSIS
  Keep the remote-control chain up: the aglink-web daemon, the aglink-screen
  HTTP server, and the SSH reverse tunnel that carries both to a remote box.

.DESCRIPTION
  Idempotent and self-healing: it measures what is actually reachable and starts
  only what is missing, so it is safe to run every few minutes from a logon task.

  Three things must be alive for a remote Claude session to drive this desktop:

    1. aglink-web serve   — 127.0.0.1:48219, the browser bridge
    2. aglink-screen serve — 127.0.0.1:48220, the screen/keyboard/mouse bridge
    3. an SSH reverse tunnel forwarding each of those ports to the remote box

  Both servers must run in the INTERACTIVE session. aglink-screen synthesizes
  input and captures the screen, neither of which a session-0 Windows service
  can do, so this is a logon task and never a service.

  The tunnel is checked by asking the remote end whether the port answers, not
  by looking for an ssh process. A process check would pass in the failure mode
  that actually bites: VS Code's own connection holds the port (its RemoteForward
  comes from the Host alias in ~/.ssh/config), our ssh stays connected but
  forwards nothing, and when that VS Code window closes the port dies with it
  while our "healthy-looking" process lingers.

  Ports land on the REMOTE loopback, which is per-machine, not per-user — so one
  tunnel serves every account on that host. Each account still needs its own MCP
  registration (names per docs/mcp-names.md — "-remote" means "the Windows PC"):

    claude mcp add --transport http --scope user aglink-web-remote    http://127.0.0.1:48219/mcp
    claude mcp add --transport http --scope user aglink-screen-remote http://127.0.0.1:48220/mcp

.PARAMETER SshHost
  SSH target(s) to try for the tunnel, in order; the first that connects wins.
  Pass `user@host`, NOT a ~/.ssh/config Host alias: an alias inherits that
  block's own RemoteForward lines, so with ExitOnForwardFailure below, one port
  already held by a VS Code connection would kill the tunnel for the other port
  too. A bare user@host matches no Host block and carries only our own -R.

  Kept a parameter (not a constant) because this repo is public — host addresses
  and accounts belong in the scheduled task's arguments, not in the tree.

.PARAMETER RepoRoot
  Path to the aglink checkout holding web\aglink-web.exe and
  screen\aglink-screen.exe. Defaults to this script's parent directory.

.EXAMPLE
  powershell -NoProfile -ExecutionPolicy Bypass -File aglink-always-on.ps1 `
    -SshHost my-box-alice,my-box-bob
#>
[CmdletBinding()]
param(
    # Required for the server/tunnel mode; ignored (and unnecessary) with -Host_/-Screen_,
    # which touches nothing remote. Validated in the body rather than declared
    # Mandatory so the host task's command line carries no address at all.
    [string[]] $SshHost,

    # Left empty and resolved in the body: Windows PowerShell 5.1 has not yet
    # populated $PSScriptRoot while it binds parameter defaults.
    [string] $RepoRoot,

    # Ports to keep alive locally and forwarded remotely. Each is healed
    # independently: one tunnel per port, so a port already held by someone
    # else's connection never blocks the other from being restored.
    [int[]] $Ports = @(48219, 48220),

    # Report what it would do and change nothing. Use it to confirm the checks
    # agree with reality before letting the scheduled task act on them.
    [switch] $CheckOnly,

    # Also keep the aglink host (the Telegram bot) alive.
    #
    # Off by default and driven by its OWN scheduled task, because the host
    # re-launches itself elevated when screen_control.elevated is set. Started
    # from an unelevated task it would raise a UAC prompt on every check, so its
    # task runs at RunLevel Highest — which is why it is a separate task from the
    # one that starts aglink-web (which stays unelevated).
    [switch] $Host_,

    # Keep ONLY the aglink-screen daemon alive, and expect to be run elevated.
    #
    # Its own scheduled task at RunLevel Highest, like -Host_. aglink-screen runs
    # elevated so that launch_app's children inherit administrator rights with no
    # UAC prompt — the whole point of unattended control. aglink-web deliberately
    # does NOT move here: an elevated browser bridge would open the user's Chrome
    # and apps elevated, which breaks more than it helps.
    [switch] $Screen_,

    # Path to the host binary; only read when -Host_ is given.
    [string] $HostExe,

    # The host's health endpoint (host/health.go); only read with -Host_.
    [string] $HostHealthUrl = 'http://127.0.0.1:27269/health',

    # Restart a running host after this many consecutive unhealthy checks.
    # With the task's 5-minute period, 3 is ~15 minutes of a stuck poll loop —
    # long enough not to restart over a network blip, short of eleven days.
    [int] $HostUnhealthyLimit = 3,

    # Hard cap on a single ssh probe. Nothing this script asks a remote box
    # should take anywhere near this long, so exceeding it means the link is
    # wedged, not slow.
    [int] $ProbeTimeoutSec = 20
)

$ErrorActionPreference = 'Stop'

if (-not $RepoRoot) { $RepoRoot = Split-Path -Parent $PSScriptRoot }

# `powershell -File` passes arguments as literal strings, so "a@h,b@h" arrives as
# ONE element rather than two — the form a scheduled task's argument line most
# naturally produces. Split here so both "-SshHost a,b" and "-SshHost a b" work.
$SshHost = @($SshHost | ForEach-Object { $_ -split ',' } | ForEach-Object { $_.Trim() } | Where-Object { $_ })

$LogPath = Join-Path $env:USERPROFILE '.aglink\always-on.log'

# Single-instance guard.
#
# The scheduled task launches this through a windowless wscript shim that
# returns as soon as it has spawned us, so Task Scheduler believes the run
# finished immediately and its MultipleInstances=IgnoreNew never applies. Two
# runs could then overlap — the SSH probes take seconds each — and both decide
# the same tunnel is down, or race in Start-Daemon between the "is it running?"
# check and the launch. A mutex costs nothing and removes the whole class.
#
# Session-local: the task runs as the logged-on user, so there is no need for
# Global\ and the privilege it would require.
$mutex = New-Object System.Threading.Mutex($false, 'Local\aglink-always-on')
$held = $false
try {
    $held = $mutex.WaitOne(0)
} catch [System.Threading.AbandonedMutexException] {
    # The previous holder exited without releasing (an early `exit`, or it was
    # killed). .NET hands the mutex over anyway and only raises this to say the
    # state it guarded may be half-finished — which for us it cannot be, since
    # every step re-measures before acting. So take it and carry on.
    $held = $true
}
if (-not $held) {
    # Another run is mid-flight. Silent: this is the normal, healthy outcome of
    # a slow probe overlapping the next tick, not something to log every time.
    exit 0
}

# Write only when something was actually done or found wrong. A line per quiet
# check would bury the events worth reading under thousands of "all fine".
function Write-Act([string] $Message) {
    $dir = Split-Path -Parent $LogPath
    if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
    "{0}  {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $Message | Add-Content -Path $LogPath -Encoding utf8
    Write-Host $Message
}

# Test-LocalPort asks the server itself, not the socket. A bound-but-wedged
# process still accepts TCP, so /health is the honest question.
function Test-LocalPort([int] $Port) {
    try {
        $r = Invoke-WebRequest -UseBasicParsing -TimeoutSec 4 "http://127.0.0.1:$Port/health"
        return $r.Content.Trim() -eq 'ok'
    } catch { return $false }
}

# Find-Daemon reports whether a `<exe> serve` process is already running,
# regardless of who started it.
function Find-Daemon([string] $Exe) {
    $leaf = Split-Path -Leaf $Exe
    return Get-CimInstance Win32_Process -Filter "Name='$leaf'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -match '\bserve\b' } |
        Select-Object -First 1
}

function Start-Daemon([string] $Exe, [string[]] $ExeArgs, [int] $Port, [string] $Label) {
    if (Test-LocalPort $Port) { return }

    # A failed health check is NOT sufficient reason to launch. These daemons are
    # long-lived and shared — other sessions hold live connections to them — so a
    # transient timeout must never spawn a second instance that fights for the
    # port. If a serve process is already there, say so and leave it to a human:
    # starting a rival is worse than the wedge we would be trying to fix.
    $existing = Find-Daemon $Exe
    if ($existing) {
        Write-Act "STALLED  $Label pid $($existing.ProcessId) is running but 127.0.0.1:$Port did not answer — left alone, not restarted"
        return
    }

    if (-not (Test-Path $Exe)) {
        Write-Act "MISSING  $Label : $Exe not found"
        return
    }
    if ($CheckOnly) {
        Write-Act "WOULD    start $Label on 127.0.0.1:$Port"
        return
    }
    Start-Process -FilePath $Exe -ArgumentList $ExeArgs -WindowStyle Hidden | Out-Null
    Start-Sleep -Seconds 2
    if (Test-LocalPort $Port) {
        Write-Act "STARTED  $Label on 127.0.0.1:$Port"
    } else {
        Write-Act "FAILED   $Label did not answer on 127.0.0.1:$Port after start"
    }
}

# Test-RemotePort asks the far end whether the forwarded port answers — the only
# check that distinguishes a live tunnel from a connected-but-not-forwarding one.
#
# ClearAllForwardings keeps the probe from trying to set up forwardings of its
# own; without it the probe both competes for the ports and prints "remote port
# forwarding failed" warnings that get mixed into the answer we are reading.
#
# Windows PowerShell 5.1 turns a native command's stderr into ErrorRecords, and
# under $ErrorActionPreference='Stop' that makes any ssh diagnostic — including
# harmless ones — throw. So relax the preference around the call and drop the
# ErrorRecords by type instead of redirecting the stream away.
# ConnectTimeout only bounds the TCP handshake. A session that connects and then
# stalls — a half-open link after a laptop sleeps or a network flaps — hangs
# forever, and on 2026-09-17 one such probe held this script (and its mutex) for
# 23 hours, silently disabling every later run. So the probe is also bounded on
# both sides: ssh's own keepalive gives up on a dead peer, and Wait-Process caps
# the wall clock in case ssh ignores it.
$probeSshOpts = @(
    '-o', 'BatchMode=yes',
    '-o', 'ConnectTimeout=6',
    '-o', 'ServerAliveInterval=5',
    '-o', 'ServerAliveCountMax=2',
    '-o', 'ClearAllForwardings=yes'
)

# Collects into $result and returns once, AFTER the try/finally. A `return`
# from inside a try that has a finally loses the value in Windows PowerShell
# 5.1 — the caller gets $null and then dies on .Trim(), which is exactly what an
# unreachable host produced here.
function Invoke-Probe([string] $Target, [string] $Command) {
    $result  = ''
    $outFile = [IO.Path]::GetTempFileName()
    $errFile = [IO.Path]::GetTempFileName()
    try {
        $p = Start-Process -FilePath 'ssh' -ArgumentList ($probeSshOpts + @($Target, $Command)) `
            -NoNewWindow -PassThru -RedirectStandardOutput $outFile -RedirectStandardError $errFile
        if ($p) {
            Wait-Process -Id $p.Id -Timeout $ProbeTimeoutSec -ErrorAction SilentlyContinue
            if ($p.HasExited) {
                # Cast: Get-Content -Raw yields $null for the empty file an ssh
                # that never connected leaves behind.
                $result = [string](Get-Content $outFile -Raw -ErrorAction SilentlyContinue)
            } else {
                Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
                Write-Act "TIMEOUT  ssh probe to $Target exceeded ${ProbeTimeoutSec}s — killed"
            }
        }
    } catch {
        $result = ''
    } finally {
        Remove-Item $outFile, $errFile -Force -ErrorAction SilentlyContinue
    }
    return $result
}

# The "$( )" wrapper is load-bearing, not style: Invoke-Probe comes back $null
# for a host that never answered (observed with an unreachable address), and a
# bare .Trim() on that throws and aborts the whole run — which would leave the
# daemons unattended for exactly the outage this script exists to cover.
function Test-RemotePort([string] $Target, [int] $Port) {
    return "$(Invoke-Probe $Target "curl -s -m 4 http://127.0.0.1:$Port/health")".Trim() -eq 'ok'
}

# Start-Tunnel opens one background ssh per port.
#
# ExitOnForwardFailure is deliberately set HERE and must never be set in
# ~/.ssh/config: in the config it would also apply to VS Code's Remote-SSH
# connections, and a second VS Code window to the same host — which cannot
# re-bind an already-taken port — would be killed outright. For this dedicated
# tunnel the opposite is right: if it cannot bind, it should die immediately so
# the next run retries, rather than sit there pretending to forward.
function Start-Tunnel([string] $Target, [int] $Port) {
    # NO -f. Windows OpenSSH does not background with -f the way OpenSSH on Unix
    # does: the parent never exits, so the old "run with -f, wait 20s, kill if it
    # has not exited" logic killed the very tunnel it had just set up — every
    # tick (observed as a steady stream of "TIMEOUT ... killed" with the port
    # never staying up). Instead we start a long-lived ssh that HOLDS the tunnel
    # and never expect it to exit; we poll the far end to see the forward come up
    # and leave ssh running when it does. ExitOnForwardFailure makes ssh quit by
    # itself when the remote port is already held, which the HasExited check sees.
    $sshArgs = @(
        '-N',
        '-o', 'BatchMode=yes',
        '-o', 'ExitOnForwardFailure=yes',
        '-o', 'ConnectTimeout=10',
        '-o', 'ServerAliveInterval=30',
        '-o', 'ServerAliveCountMax=3',
        '-R', "${Port}:127.0.0.1:$Port",
        $Target
    )
    $p = Start-Process -FilePath 'ssh' -ArgumentList $sshArgs -NoNewWindow -PassThru
    # Give auth + forwarding time to establish, polling the far end. As soon as
    # the port answers we return success and LEAVE ssh running — it is the tunnel.
    for ($i = 0; $i -lt 8; $i++) {
        Start-Sleep -Seconds 2
        if ($p.HasExited) { return $false } # ExitOnForwardFailure: port held / far end down
        if (Test-RemotePort $Target $Port) { return $true }
    }
    # Never came up within the window — a wedged link. Kill it so the next tick
    # retries from a clean slate rather than leaking stalled ssh processes.
    if (-not $p.HasExited) {
        Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
        Write-Act "TIMEOUT  ssh -R $Port to $Target did not come up within $((8*2))s — killed"
    }
    return $false
}

# ---- 0: the aglink host (Telegram), when asked -------------------------------
#
# Two checks. Is there a process at all — and, since a process can be alive with
# its Telegram poll loop stuck (the loop logs only failures, which is how the
# host went silent on 2026-09-07 and nobody noticed for eleven days), does its
# /health endpoint say the loop is still coming back from getUpdates?
#
# Test-HostHealth answers 'ok', 'bad' (endpoint says unhealthy, or does not
# answer in time: a hung process) or 'none' (connection refused: an older build
# without the endpoint, or AGLINK_HEALTH_ADDR=off). Only 'bad' counts towards a
# restart, so a host that simply has no endpoint is never restarted over it.

function Test-HostHealth([string] $Url) {
    try {
        $r = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 10
        if ($r.StatusCode -eq 200) { return 'ok' }
        return 'bad'
    } catch [System.Net.WebException] {
        $ex = $_.Exception
        if ($ex.Response) { return 'bad' }                                   # 503: unhealthy
        if ($ex.Status -eq [System.Net.WebExceptionStatus]::Timeout) { return 'bad' }
        return 'none'                                                        # refused: no endpoint
    } catch {
        return 'none'
    }
}

if ($Host_) {
    if (-not $HostExe) { $HostExe = Join-Path $RepoRoot 'host\aglink.exe' }
    $running = Get-CimInstance Win32_Process -Filter "Name='aglink.exe'" -ErrorAction SilentlyContinue |
        Select-Object -First 1

    # A running host whose poll loop is stuck: count consecutive bad checks in a
    # small state file and restart once the limit is reached.
    $failFile = Join-Path $env:TEMP 'aglink-host-health.fails'
    if ($running) {
        $verdict = Test-HostHealth $HostHealthUrl
        if ($verdict -eq 'bad') {
            $fails = 0
            try { $fails = [int](Get-Content $failFile -ErrorAction Stop) } catch {}
            $fails++
            if ($fails -ge $HostUnhealthyLimit) {
                if ($CheckOnly) {
                    Write-Act "WOULD    restart aglink host: unhealthy $fails checks in a row"
                } else {
                    Write-Act "RESTART  aglink host pid $($running.ProcessId): /health unhealthy $fails checks in a row"
                    Get-CimInstance Win32_Process -Filter "Name='aglink.exe' OR Name='aglink-chat.exe'" -ErrorAction SilentlyContinue |
                        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
                    Start-Sleep -Seconds 2
                    $running = $null   # falls through to the start below
                }
                Remove-Item $failFile -ErrorAction SilentlyContinue
            } else {
                Set-Content -Path $failFile -Value $fails
                Write-Act "UNHEALTHY aglink host /health check $fails/$HostUnhealthyLimit"
            }
        } else {
            Remove-Item $failFile -ErrorAction SilentlyContinue
        }
    }

    if (-not $running) {
        if (-not (Test-Path $HostExe)) {
            Write-Act "MISSING  aglink host: $HostExe not found"
        } elseif ($CheckOnly) {
            Write-Act "WOULD    start aglink host"
        } else {
            Start-Process -FilePath $HostExe -ArgumentList 'run' `
                -WorkingDirectory (Split-Path -Parent $HostExe) -WindowStyle Hidden | Out-Null
            Start-Sleep -Seconds 5
            $now = Get-CimInstance Win32_Process -Filter "Name='aglink.exe'" -ErrorAction SilentlyContinue |
                Select-Object -First 1
            if ($now) {
                Write-Act "STARTED  aglink host pid $($now.ProcessId)"
            } else {
                # Expected transiently: the first process elevates and exits,
                # and the elevated one takes a moment to appear. A run that is
                # genuinely failing says so again on the next tick.
                Write-Act "PENDING  aglink host launched but no process yet (elevating?)"
            }
        }
    }
    # The host task does only the host: the web server belongs to the unelevated
    # task, the screen server to its own -Screen_ task.
    $mutex.ReleaseMutex()
    exit 0
}

# ---- aglink-screen, elevated, when asked -------------------------------------
#
# Started from the RunLevel=Highest "aglink screen always-on" task so the daemon
# is elevated and the programs it launches inherit that without a UAC prompt.
# Start-Daemon inherits this process's elevation, so nothing else is needed here.
if ($Screen_) {
    Start-Daemon (Join-Path $RepoRoot 'screen\aglink-screen.exe') @('serve') 48220 'aglink-screen'
    $mutex.ReleaseMutex()
    exit 0
}

if (-not $SshHost) {
    Write-Act "USAGE    -SshHost is required unless -Host_ or -Screen_ is given"
    $mutex.ReleaseMutex()
    exit 2
}

# ---- 1: the local web server --------------------------------------------------
#
# aglink-screen is NOT started here — it runs elevated under its own task
# (-Screen_). This task still forwards its port 48220 through the tunnel below;
# the tunnel does not care which process, or which elevation, owns the listener.

Start-Daemon (Join-Path $RepoRoot 'web\aglink-web.exe') @('serve') 48219 'aglink-web'

# ---- 3: the tunnel ------------------------------------------------------------
#
# Find one reachable host alias and heal every port through it. Trying each
# alias per port instead would risk splitting the ports across two accounts —
# harmless today (the remote loopback is shared) but confusing to debug later.

$live = $null
foreach ($h in $SshHost) {
    if ("$(Invoke-Probe $h 'echo up')".Trim() -eq 'up') { $live = $h; break }
}

if (-not $live) {
    Write-Act ("UNREACHABLE  no SSH host answered: {0}" -f ($SshHost -join ', '))
    exit 0
}

foreach ($p in $Ports) {
    if (Test-RemotePort $live $p) { continue }
    if ($CheckOnly) {
        Write-Act "WOULD    open tunnel ${live}:$p"
        continue
    }
    if (Start-Tunnel $live $p) {
        Write-Act "TUNNEL   ${live}:$p restored"
    } else {
        # Expected and harmless while a VS Code connection still holds the port:
        # that connection is forwarding it, so the remote side works and the next
        # run's Test-RemotePort will pass. Logged because the same line means a
        # real outage when the port genuinely is not answering.
        Write-Act "TUNNEL   ${live}:$p could not be bound (held by another connection, or the far end is down)"
    }
}

$mutex.ReleaseMutex()
