<#
.SYNOPSIS
  Builds bot.exe/agent.exe (windowless) and registers them as Windows
  Scheduled Tasks that start automatically when you log in.

.DESCRIPTION
  Run this once, from any PowerShell window, after you've set up .env and
  projects.json. It:
    1. Builds bot.exe and agent.exe with -H=windowsgui, so no console
       window ever appears for them.
    2. Redirects their logs to bot.log / agent.log (a windowsgui binary
       has no console to print to otherwise).
    3. Registers two Scheduled Tasks, triggered "at log on" for your user,
       that restart automatically a few times if either process crashes.

  Safe to re-run — existing tasks with the same names are replaced.
#>

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Write-Host "Project root: $root"

Write-Host "Building bot.exe and agent.exe (windowless)..."
Push-Location $root
try {
    go build -ldflags "-H=windowsgui" -o bot.exe ./bot
    go build -ldflags "-H=windowsgui" -o agent.exe ./agent
} finally {
    Pop-Location
}

function Register-ClaudeAnywhereTask {
    param(
        [string]$Name,
        [string]$ExePath,
        [string]$Arguments
    )

    $action = New-ScheduledTaskAction -Execute $ExePath -Argument $Arguments -WorkingDirectory $root
    $trigger = New-ScheduledTaskTrigger -AtLogOn
    # ExecutionTimeLimit 0 means "no time limit" — these processes are meant to run forever.
    $settings = New-ScheduledTaskSettingsSet `
        -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
        -ExecutionTimeLimit (New-TimeSpan -Days 0) `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries

    Register-ScheduledTask -TaskName $Name -Action $action -Trigger $trigger -Settings $settings `
        -User $env:USERNAME -RunLevel Limited -Force | Out-Null

    Write-Host "Registered task: $Name"
}

Register-ClaudeAnywhereTask -Name "ClaudeAnywhere-Bot" -ExePath "$root\bot.exe" -Arguments "-logfile bot.log"
Register-ClaudeAnywhereTask -Name "ClaudeAnywhere-Agent" -ExePath "$root\agent.exe" -Arguments "-logfile agent.log"

Write-Host ""
Write-Host "Done. Both tasks will start next time you log in." -ForegroundColor Green
Write-Host "To start them right now without logging out: Start-ScheduledTask -TaskName ClaudeAnywhere-Bot; Start-ScheduledTask -TaskName ClaudeAnywhere-Agent"
Write-Host "Logs: $root\bot.log and $root\agent.log"
Write-Host "To remove: .\scripts\uninstall-autostart.ps1"
