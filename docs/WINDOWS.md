# Windows companion

OpticDeck runs its virtual desktop and web server on Linux. The experimental
Windows companion uses Microsoft UI Automation as a local command line tool. It
can inspect an app's exposed accessibility controls and invoke one selected
control. It works independently of the browser viewer.

Use Windows PowerShell 5.1 (`powershell.exe`) in a signed-in Windows desktop
session. Open the target app under the same Windows account. The companion
operates on the process's visible main window. Apps with several processes may
expose fewer controls because the scan stays within the selected process.

From the repository root, find the process you want to inspect and pass its PID.

```powershell
Get-Process | Where-Object MainWindowTitle | Format-Table Id, ProcessName, MainWindowTitle
$targetId = 1234   # Replace with the target app's PID.
$helper = '.\cmd\opticdeck-win\opticdeck-win.ps1'
$tree = powershell.exe -NoProfile -NonInteractive -MTA -File $helper -ProcessId $targetId |
    ConvertFrom-Json
$tree.nodes | Format-Table name, control_type, runtime_id, can_invoke
```

`Inspect` emits a JSON object with scope information and a flat list of
controls. `parent_index` is a zero-based index into that list and is null for
the window root. `-MaxNodes` and `-MaxDepth` bound the scan and `truncated`
indicates that the scan reached either limit. When inspecting a larger app,
raise the limits up to 250 nodes and 32 levels.

To activate a control, find a row with `can_invoke = true` in a fresh
inspection. Pass its `runtime_id` and the scope values from that same result.
Use a harmless test control while learning the workflow.

```powershell
$runtimeId = '42.1234'   # Replace with the runtime_id from the chosen row.
$invokeArgs = @(
    '-Action', 'Invoke', '-ProcessId', $tree.process_id,
    '-WindowHandle', $tree.window_handle, '-ProcessStart', $tree.process_start,
    '-RuntimeId', $runtimeId
)
powershell.exe -NoProfile -NonInteractive -MTA -File $helper @invokeArgs
```

Repeat the same scan limits during invocation if you customized them for
inspection. Runtime IDs are transient. Inspect again after changes to the
window. The companion checks the process, window and control before requesting
the native Invoke action. An app can defer the effect of that request.

The script runs as a local UI Automation client. Its JSON contains control
metadata, and its only action calls the selected control's native Invoke
pattern. It masks labels on controls that report they are password fields.
Other app-provided names and titles can still be private. Windows session
boundaries, elevation, and the app's accessibility provider can limit what the
script can inspect or invoke. [Microsoft's UI Automation overview for
tests](https://learn.microsoft.com/dotnet/framework/ui-automation/using-ui-automation-for-automated-testing)
and [runtime ID lifetime](https://learn.microsoft.com/dotnet/api/system.windows.automation.automationelement.getruntimeid?view=netframework-4.8).

## Native smoke test

Run this on an interactive Windows desktop. The test opens a small Windows
Forms fixture, checks the scan cap and password masking, and invokes the
fixture's test button.

```powershell
powershell.exe -NoProfile -NonInteractive -MTA -File .\cmd\opticdeck-win\smoke.ps1
```

The scripts were parsed on Linux. Native Windows behavior needs a successful
run of the smoke test on Windows.
