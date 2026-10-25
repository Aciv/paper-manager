# Execute the actual CI wrapper with fake processes/accounts and real fixture
# files. No local accounts or application services are created by this test.
$ErrorActionPreference = 'Stop'
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('pm-harness-' + [guid]::NewGuid().ToString('N'))
$savedEnvironment = @{}
foreach ($name in @('PUBLIC', 'GITHUB_ACTIONS', 'GITHUB_WORKSPACE')) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name)
}
$wrapper = Join-Path $PSScriptRoot 'check_windows_desktop_user.ps1'
$harnessTestState = [pscustomobject]@{
    Calls = [Collections.Generic.List[object]]::new()
    RemovedAccounts = 0
}

function New-LocalUser { param($Name, $Password, [switch]$AccountNeverExpires) }
function Get-LocalGroupMember { param($SID) }
function Add-LocalGroupMember { param($SID, $Member) }
function Remove-LocalUser { param($Name, $ErrorAction) $harnessTestState.RemovedAccounts++ }
function Get-Acl {
    param($Path)
    $acl = [pscustomobject]@{}
    $acl | Add-Member -MemberType ScriptMethod -Name SetAccessRuleProtection -Value { param($Enabled, $Preserve) }
    $acl | Add-Member -MemberType ScriptMethod -Name AddAccessRule -Value { param($Rule) }
    return $acl
}
function Set-Acl { param($Path, $AclObject) }
function Get-Command { param($Name) return [pscustomobject]@{ Source = 'fixture-python.exe' } }

function Start-Process {
    param($FilePath, $ArgumentList, $Credential, [switch]$LoadUserProfile,
          $WorkingDirectory, [switch]$Wait, [switch]$PassThru,
          $RedirectStandardOutput, $RedirectStandardError)
    $harnessTestState.Calls.Add($ArgumentList)
    foreach ($name in @('TEMP', 'TMP', 'USERPROFILE', 'HOME', 'APPDATA', 'LOCALAPPDATA')) {
        $value = [Environment]::GetEnvironmentVariable($name)
        if (-not $value.StartsWith($WorkingDirectory + [IO.Path]::DirectorySeparatorChar)) {
            throw "Child inherited an unowned environment path: $name"
        }
        if (-not (Test-Path -LiteralPath $value -PathType Container)) {
            throw "Child environment directory missing: $name"
        }
    }
    $outputIndex = [array]::IndexOf($ArgumentList, '--output-dir')
    if ($outputIndex -lt 0) { throw 'Output directory missing' }
    $resultPath = $ArgumentList[$outputIndex + 1].Trim('"')
    New-Item -ItemType Directory -Path $resultPath -Force | Out-Null
    $isUpgradeProbe = $ArgumentList[0] -like '*check_desktop_upgrade.py*'
    $isAscii = $ArgumentList -contains '--ascii-paths'
    $record = @{ status = 'passed'; is_admin = $false }
    $exitCode = 0
    if (-not $isAscii) {
        $record.status = 'failed'
        $record.error = 'PostgreSQL needs an ASCII path on this volume; choose ASCII installation/data directories'
        $exitCode = 1
    }
    $name = if ($isUpgradeProbe) { 'upgrade.json' } else { 'launcher.json' }
    [IO.File]::WriteAllText((Join-Path $resultPath $name), ($record | ConvertTo-Json))
    [IO.File]::WriteAllText($RedirectStandardOutput, 'fixture output')
    [IO.File]::WriteAllText($RedirectStandardError, '')
    return [pscustomobject]@{ ExitCode = $exitCode }
}

try {
    New-Item -ItemType Directory -Path $fixture | Out-Null
    $env:PUBLIC = $fixture
    $env:GITHUB_ACTIONS = 'true'
    $env:GITHUB_WORKSPACE = $fixture
    $delivery = Join-Path $fixture ('delivery ' + [char]0x9605)
    $bundle = Join-Path $delivery 'bundle'
    $harness = Join-Path $delivery 'validation'
    New-Item -ItemType Directory -Path $bundle, $harness | Out-Null
    [IO.File]::WriteAllText((Join-Path $bundle 'bundle.json'), '{}')
    $output = Join-Path $fixture 'results'
    & $wrapper -Bundle $bundle -Harness $harness -OutputDirectory $output -Upgrade
    if ($harnessTestState.Calls.Count -ne 3) { throw 'Expected Unicode refusal, ASCII runtime and upgrade probes' }
    foreach ($name in @('launcher.json', 'upgrade/upgrade.json')) {
        $record = Get-Content -LiteralPath (Join-Path $output $name) -Raw | ConvertFrom-Json
        if ($record.status -ne 'passed') { throw "Missing passed result: $name" }
    }
    $refusal = Get-Content -LiteralPath (Join-Path $output 'unicode-path/launcher.json') -Raw | ConvertFrom-Json
    if ($refusal.status -ne 'failed') { throw 'Original Unicode refusal was not preserved' }
    if ($harnessTestState.RemovedAccounts -ne 1) { throw 'Account cleanup did not run' }

    # Prove this regression test catches the original case-insensitive variable
    # collision, rather than merely exercising a different wrapper path.
    $broken = Join-Path $fixture 'old-wrapper.ps1'
    $source = [IO.File]::ReadAllText($wrapper).Replace('$upgradeProcess', '$upgrade')
    [IO.File]::WriteAllText($broken, $source)
    $harnessTestState.Calls.Clear()
    $caught = $false
    try {
        & $broken -Bundle $bundle -Harness $harness -OutputDirectory (Join-Path $fixture 'old-results') -Upgrade
    } catch {
        $caught = $_.Exception.Message -like '*SwitchParameter*'
    }
    if (-not $caught) { throw 'Regression test did not catch the old typed-switch assignment' }
    Write-Output 'PASS: actual wrapper collects runtime/upgrade results and preserves Unicode refusal'
} finally {
    foreach ($name in $savedEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name])
    }
    if (Test-Path -LiteralPath $fixture) { Remove-Item -LiteralPath $fixture -Recurse -Force }
}
