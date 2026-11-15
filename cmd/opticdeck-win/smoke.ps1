# Run in a signed-in Windows desktop session, using Windows PowerShell 5.1.
[CmdletBinding()]
param([switch] $Fixture)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

if ($Fixture) {
    Add-Type -AssemblyName PresentationFramework, PresentationCore, WindowsBase
    $window = [Windows.Window]::new()
    $window.Title = 'OpticDeck smoke fixture'
    $window.Width = 340
    $window.Height = 155
    $window.Topmost = $true
    $window.WindowStartupLocation = 'CenterScreen'
    $layout = [Windows.Controls.StackPanel]::new()
    $layout.Margin = [Windows.Thickness]::new(14)

    # WPF exposes the native Invoke and password semantics through UI Automation peers.
    $button = [Windows.Controls.Button]::new()
    $button.Content = 'Run smoke test'
    $button.Height = 28
    [Windows.Automation.AutomationProperties]::SetAutomationId($button, 'OpticDeckSmokeButton')
    $button.Add_Click({ $window.Title = 'OpticDeck smoke invoked' })
    [void] $layout.Children.Add($button)

    $password = [Windows.Controls.PasswordBox]::new()
    $password.Password = 'opticdeck-fixture-masked-text'
    $password.Height = 24
    $password.Margin = [Windows.Thickness]::new(0, 10, 0, 0)
    [Windows.Automation.AutomationProperties]::SetName($password, 'OpticDeck smoke password label')
    [void] $layout.Children.Add($password)
    $window.Content = $layout
    [void] $window.ShowDialog()
    return
}

$engine = (Get-Process -Id $PID).Path
$fixtureProcess = Start-Process -FilePath $engine -ArgumentList @(
    '-NoProfile', '-NonInteractive', '-STA', '-File', ('"{0}"' -f $PSCommandPath), '-Fixture'
) -PassThru
$helper = Join-Path $PSScriptRoot 'opticdeck-win.ps1'

try {
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    do {
        $fixtureProcess.Refresh()
        if ($fixtureProcess.HasExited) { throw 'The Windows fixture exited before opening a window.' }
        if ($fixtureProcess.MainWindowTitle -eq 'OpticDeck smoke fixture') { break }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($fixtureProcess.MainWindowTitle -ne 'OpticDeck smoke fixture') { throw 'The fixture window did not appear.' }

    $limitedJson = & $engine -NoProfile -NonInteractive -MTA -File $helper -ProcessId $fixtureProcess.Id -MaxNodes 1
    if ($LASTEXITCODE -ne 0) { throw 'Bounded inspection failed.' }
    $limited = $limitedJson | ConvertFrom-Json
    if (!$limited.truncated -or @($limited.nodes).Count -ne 1) { throw 'The node limit was not respected.' }

    $deadline = [DateTime]::UtcNow.AddSeconds(9)
    do {
        $json = & $engine -NoProfile -NonInteractive -MTA -File $helper -ProcessId $fixtureProcess.Id
        if ($LASTEXITCODE -ne 0) { throw 'Inspection failed.' }
        $inspection = $json | ConvertFrom-Json
        $button = @($inspection.nodes | Where-Object { $_.automation_id -ceq 'OpticDeckSmokeButton' })
        if ($button.Count -eq 1 -and $button[0].can_invoke) { break }
        Start-Sleep -Milliseconds 150
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($button.Count -ne 1 -or !$button[0].can_invoke -or $button[0].name -cne 'Run smoke test') {
        Write-Host "Synthetic fixture inspection: $json"
        throw 'The fixture button was not discoverable and invokable.'
    }
    $protected = @($inspection.nodes | Where-Object { $_.protected })
    if ($protected.Count -ne 1 -or $json -match 'opticdeck-fixture-masked-text|OpticDeck smoke password label') {
        Write-Host "Synthetic fixture inspection: $json"
        throw 'The fixture password control was not redacted.'
    }
    $result = & $engine -NoProfile -NonInteractive -MTA -File $helper -Action Invoke -ProcessId $fixtureProcess.Id `
        -ProcessStart $inspection.process_start -WindowHandle $inspection.window_handle -RuntimeId $button[0].runtime_id
    if ($LASTEXITCODE -ne 0 -or !($result | ConvertFrom-Json).ok) { throw 'Invocation failed.' }

    $deadline = [DateTime]::UtcNow.AddSeconds(5)
    do {
        $fixtureProcess.Refresh()
        if ($fixtureProcess.MainWindowTitle -eq 'OpticDeck smoke invoked') { break }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($fixtureProcess.MainWindowTitle -ne 'OpticDeck smoke invoked') { throw 'The fixture click did not take effect.' }
    Write-Output 'PASS Windows UI Automation: scoped inspection, scan cap, password masking, and native invocation.'
} finally {
    if (!$fixtureProcess.HasExited) {
        [void] $fixtureProcess.CloseMainWindow()
        if (!$fixtureProcess.WaitForExit(3000)) { $fixtureProcess.Kill() }
    }
    $fixtureProcess.Dispose()
}
