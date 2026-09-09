' Launch a command with no console window at all.
'
' Task Scheduler running powershell.exe as the logged-on user flashes a console
' for a fraction of a second on every run, even with -WindowStyle Hidden: the
' window is created and then hidden, so it is visible in between. On a task that
' fires every few minutes that flicker is constant and looks like something is
' wrong with the machine.
'
' WScript.Shell's Run with intWindowStyle=0 never creates the window in the
' first place, and wscript.exe itself is windowless, so nothing appears.
'
' Usage (from the scheduled task's action):
'   wscript.exe //B //Nologo <path>\run-hidden.vbs "<command line to run>"
'
' The whole command line is ONE argument — quote it in the task action.

Option Explicit

Dim args, shell
Set args = WScript.Arguments

If args.Count < 1 Then
    ' No console to print to, so fail with a non-zero exit code the task will
    ' record rather than a message nobody would see.
    WScript.Quit 2
End If

Set shell = CreateObject("WScript.Shell")
' 0 = hidden window, False = do not wait; the task is fire-and-forget and the
' script it starts does its own logging.
shell.Run args(0), 0, False
WScript.Quit 0
