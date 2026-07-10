<#
.SYNOPSIS
  Removes the ClaudeAnywhere-Bot / ClaudeAnywhere-Agent scheduled tasks
  created by install-autostart.ps1.
#>

$ErrorActionPreference = "SilentlyContinue"

foreach ($name in "ClaudeAnywhere-Bot", "ClaudeAnywhere-Agent") {
    $existing = Get-ScheduledTask -TaskName $name
    if ($existing) {
        Stop-ScheduledTask -TaskName $name
        Unregister-ScheduledTask -TaskName $name -Confirm:$false
        Write-Host "Removed task: $name"
    } else {
        Write-Host "Task not found (already removed?): $name"
    }
}
