' Start the aglink-web daemon with no console window.
'
' Registered under HKCU\...\Run by aglink-web-Setup.exe so the daemon is up at
' logon — the Chrome and VS Code extensions connect to it on their own, and a
' console program started from Run would flash a window every logon.
'
' If a daemon is already running, the second `serve` fails to bind the port
' and exits at once, so running this twice is harmless.
Option Explicit
Dim fso, shell, dir
Set fso = CreateObject("Scripting.FileSystemObject")
Set shell = CreateObject("WScript.Shell")
dir = fso.GetParentFolderName(WScript.ScriptFullName)
shell.Run """" & dir & "\aglink-web.exe"" serve", 0, False
