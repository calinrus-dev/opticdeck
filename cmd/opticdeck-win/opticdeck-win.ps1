# Inspect or invoke UI Automation controls within one local process's main window.
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateRange(1, 2147483647)]
    [int] $ProcessId,

    [ValidateSet('Inspect', 'Invoke')]
    [string] $Action = 'Inspect',

    [long] $WindowHandle,

    [ValidatePattern('^[0-9]{1,19}$')]
    [string] $ProcessStart,

    [ValidateLength(1, 384)]
    [ValidatePattern('^-?[0-9]+(\.-?[0-9]+)*$')]
    [string] $RuntimeId,

    [ValidateRange(1, 250)]
    [int] $MaxNodes = 120,

    [ValidateRange(1, 32)]
    [int] $MaxDepth = 16
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

function Limit-Text([string] $Text) {
    if ($Text.Length -le 256) { return $Text }
    $length = 255
    if ([char]::IsHighSurrogate($Text[$length - 1])) { $length-- }
    return $Text.Substring(0, $length) + [char]0x2026
}

try {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        throw 'This helper requires Windows PowerShell 5.1 on Windows.'
    }
    if ($PSVersionTable.PSEdition -ne 'Desktop' -or $PSVersionTable.PSVersion -lt [version]'5.1') {
        throw 'Run this helper with Windows PowerShell 5.1 (powershell.exe).'
    }
    if ($Action -eq 'Invoke' -and ($WindowHandle -eq 0 -or !$ProcessStart -or !$RuntimeId)) {
        throw 'Invoke requires WindowHandle, ProcessStart, and RuntimeId from a recent Inspect.'
    }
    if ($Action -eq 'Inspect' -and ($ProcessStart -or $RuntimeId)) {
        throw 'ProcessStart and RuntimeId are used with Invoke.'
    }
    if ($ProcessId -eq $PID) { throw 'Choose the process ID of a separate app with an open window.' }
    $targetProcess = Get-Process -Id $ProcessId
    $hostProcess = Get-Process -Id $PID
    if ($hostProcess.SessionId -eq 0 -or $targetProcess.SessionId -ne $hostProcess.SessionId) {
        throw 'The target window must be on the current interactive Windows session.'
    }
    $start = $targetProcess.StartTime.ToUniversalTime().Ticks.ToString([Globalization.CultureInfo]::InvariantCulture)
    $handle = $targetProcess.MainWindowHandle
    if ($handle -eq [IntPtr]::Zero) { throw 'The target process has no visible main window.' }
    if ($WindowHandle -ne 0 -and $WindowHandle -ne $handle.ToInt64()) {
        throw 'The main window changed. Inspect the current window before proceeding.'
    }
    if ($Action -eq 'Invoke' -and $ProcessStart -cne $start) {
        throw 'The process instance changed. Inspect the current window before proceeding.'
    }

    Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
    $root = [System.Windows.Automation.AutomationElement]::FromHandle($handle)
    if ($null -eq $root -or $root.Current.ProcessId -ne $ProcessId) {
        throw 'The main window is unavailable or no longer belongs to the target process.'
    }

    $walker = [System.Windows.Automation.TreeWalker]::RawViewWalker
    $pending = [Collections.Generic.Stack[object]]::new()
    $pending.Push(@{ element = $root; level = 0; parent_index = $null })
    $rows = [Collections.Generic.List[object]]::new()
    $visited = 0
    $truncated = $false
    $match = $null

    while ($pending.Count -gt 0 -and $visited -lt $MaxNodes) {
        $item = $pending.Pop()
        $visited++
        $element = $item.element
        if ($item.level -gt 0) {
            $sibling = $walker.GetNextSibling($element)
            if ($null -ne $sibling) {
                $pending.Push(@{ element = $sibling; level = $item.level; parent_index = $item.parent_index })
            }
        }

        $info = $element.Current
        if ($info.ProcessId -ne $ProcessId) { continue }
        $protected = $info.IsPassword
        $id = $element.GetRuntimeId() -join '.'
        if ($id.Length -gt 384) { $id = $null }

        if ($Action -eq 'Invoke' -and $id -ceq $RuntimeId -and $item.level -gt 0) {
            $match = $element
            break
        }

        $rowIndex = $null
        if ($Action -eq 'Inspect') {
            $rowIndex = $rows.Count
            $row = [ordered]@{
                parent_index = $item.parent_index
                runtime_id = $id
                control_type = Limit-Text $info.ControlType.ProgrammaticName
                name = $null
                automation_id = $null
                enabled = $info.IsEnabled
                visible = !$info.IsOffscreen
                protected = $protected
                can_invoke = $false
            }
            if (!$protected) {
                $row.name = Limit-Text $info.Name
                $row.automation_id = Limit-Text $info.AutomationId
                if ($row.enabled -and $row.visible -and $item.level -gt 0 -and $id) {
                    $candidatePattern = $null
                    $row.can_invoke = $element.TryGetCurrentPattern(
                        [System.Windows.Automation.InvokePattern]::Pattern, [ref]$candidatePattern)
                }
            }
            $rows.Add($row)
        }

        if ($protected) { continue }
        $child = $walker.GetFirstChild($element)
        if ($null -ne $child) {
            if ($item.level -eq $MaxDepth) { $truncated = $true }
            else { $pending.Push(@{ element = $child; level = $item.level + 1; parent_index = $rowIndex }) }
        }
    }

    if ($Action -eq 'Inspect') {
        [ordered]@{
            process_id = $ProcessId
            process_start = $start
            window_handle = $handle.ToInt64()
            truncated = $truncated -or $pending.Count -gt 0
            nodes = $rows.ToArray()
        } | ConvertTo-Json -Depth 5 -Compress
        return
    }

    if ($null -eq $match) { throw 'The control was not found within this window and the current scan limits.' }
    $targetProcess.Refresh()
    if ($targetProcess.HasExited -or $targetProcess.MainWindowHandle -ne $handle) {
        throw 'The main window changed. Inspect the current window before proceeding.'
    }
    $info = $match.Current
    if ($info.ProcessId -ne $ProcessId -or $info.IsPassword -or !$info.IsEnabled -or $info.IsOffscreen -or
        ($match.GetRuntimeId() -join '.') -cne $RuntimeId) {
        throw 'The control is unavailable for invocation. Inspect the current window before proceeding.'
    }

    $inWindow = $false
    $ancestor = $walker.GetParent($match)
    for ($level = 1; $null -ne $ancestor -and $level -le $MaxDepth; $level++) {
        if ($ancestor.Current.ProcessId -ne $ProcessId) { break }
        if ([System.Windows.Automation.Automation]::Compare($ancestor, $root)) {
            $inWindow = $true
            break
        }
        $ancestor = $walker.GetParent($ancestor)
    }
    if (!$inWindow) { throw 'The control is no longer inside the selected window.' }

    $pattern = $null
    if (!$match.TryGetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern, [ref]$pattern)) {
        throw 'The control does not support the UI Automation Invoke pattern.'
    }
    ([System.Windows.Automation.InvokePattern]$pattern).Invoke()
    [ordered]@{ ok = $true; process_id = $ProcessId; window_handle = $handle.ToInt64(); runtime_id = $RuntimeId } |
        ConvertTo-Json -Compress
} catch {
    [Console]::Error.WriteLine("OpticDeck: $(Limit-Text $_.Exception.Message)")
    exit 1
}
