# Run in a signed-in Windows desktop session, using Windows PowerShell 5.1.
[CmdletBinding()]
param([switch] $Fixture)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

if ($Fixture) {
    Add-Type -AssemblyName System.Windows.Forms, System.Drawing
    $form = [Windows.Forms.Form]::new()
    $form.Text = 'OpticDeck smoke fixture'
    $form.ClientSize = [Drawing.Size]::new(290, 100)

    $button = [Windows.Forms.Button]::new()
    $button.Text = 'Run smoke test'
    $button.Bounds = [Drawing.Rectangle]::new(14, 14, 140, 28)
    $button.Add_Click({ $form.Text = 'OpticDeck smoke invoked' })
    $form.Controls.Add($button)

    $password = [Windows.Forms.TextBox]::new()
    $password.UseSystemPasswordChar = $true
    $password.Text = 'opticdeck-fixture-masked-text'
    $password.Bounds = [Drawing.Rectangle]::new(14, 55, 220, 24)
    $form.Controls.Add($password)
    try { [Windows.Forms.Application]::Run($form) }
    finally { $form.Dispose() }
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

    $json = & $engine -NoProfile -NonInteractive -MTA -File $helper -ProcessId $fixtureProcess.Id
    if ($LASTEXITCODE -ne 0) { throw 'Inspection failed.' }
    $inspection = $json | ConvertFrom-Json
    $button = @($inspection.nodes | Where-Object { $_.name -ceq 'Run smoke test' -and $_.can_invoke })
    if ($button.Count -ne 1) { throw 'The fixture button was not discoverable and invokable.' }
    $protected = @($inspection.nodes | Where-Object { $_.protected })
    if ($protected.Count -ne 1 -or $json -match 'opticdeck-fixture-masked-text') {
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
