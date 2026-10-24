# CI-only: run the entire D2 stack as an ordinary Windows account.
param(
    [string]$RedisServer,
    [string]$Bundle,
    [string]$Harness,
    [string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
if ($env:GITHUB_ACTIONS -ne 'true') {
    throw 'Run only in an ephemeral GitHub Actions Windows runner'
}
$account = 'pm_desktop_' + ([guid]::NewGuid().ToString('N').Substring(0, 8))
$password = ConvertTo-SecureString ('Pm!' + [guid]::NewGuid().ToString('N')) -AsPlainText -Force
$credential = [pscredential]::new("$env:COMPUTERNAME\$account", $password)
$root = Join-Path $env:PUBLIC $account
$repo = $env:GITHUB_WORKSPACE
$output = if ($OutputDirectory) { $OutputDirectory } else { Join-Path $repo 'tmp/desktop-d0/launcher' }
$python = (Get-Command python).Source
if (-not $Bundle) { $bin = Split-Path (Get-Command pg_ctl).Source }
try {
    New-LocalUser -Name $account -Password $password -AccountNeverExpires | Out-Null
    $members = Get-LocalGroupMember -SID 'S-1-5-32-545'
    if (-not ($members | Where-Object { $_.Name -eq "$env:COMPUTERNAME\$account" })) {
        Add-LocalGroupMember -SID 'S-1-5-32-545' -Member $account
    }
    New-Item -ItemType Directory -Path $root | Out-Null
    $acl = Get-Acl $root
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($sid in @('S-1-5-18', 'S-1-5-32-544')) {
        $identity = [System.Security.Principal.SecurityIdentifier]::new($sid)
        $rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
            $identity, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
        $acl.AddAccessRule($rule)
    }
    $rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
        "$env:COMPUTERNAME\$account", 'Modify', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
    $acl.AddAccessRule($rule)
    Set-Acl $root $acl
    if ($Bundle) {
        $arguments = @(
            ('"' + (Join-Path $Harness 'check_desktop_launcher.py') + '"'),
            '--bundle', ('"' + $Bundle + '"'),
            '--output-dir', ('"' + (Join-Path $root 'result') + '"'),
            '--temp-root', ('"' + $root + '"')
        )
    } else {
        $arguments = @(
            ('"' + (Join-Path $repo 'scripts/check_desktop_launcher.py') + '"'),
            '--repository', ('"' + $repo + '"'),
            '--launcher', ('"' + (Join-Path $repo 'tmp/desktop-d0/bin/launcher.exe') + '"'),
            '--server', ('"' + (Join-Path $repo 'tmp/desktop-d0/bin/server.exe') + '"'),
            '--postgres-bin', ('"' + $bin + '"'),
            '--redis-server', ('"' + $RedisServer + '"'),
            '--output-dir', ('"' + (Join-Path $root 'result') + '"'),
            '--temp-root', ('"' + $root + '"')
        )
    }
    $process = Start-Process -FilePath $python -ArgumentList $arguments -Credential $credential `
        -LoadUserProfile -WorkingDirectory $root -Wait -PassThru `
        -RedirectStandardOutput (Join-Path $root 'stdout.log') `
        -RedirectStandardError (Join-Path $root 'stderr.log')
    New-Item -ItemType Directory -Path $output -Force | Out-Null
    Copy-Item (Join-Path $root 'result/*') $output -ErrorAction SilentlyContinue
    Copy-Item (Join-Path $root '*.log') $output -ErrorAction SilentlyContinue
    $report = Get-Content (Join-Path $output 'launcher.json') -Raw | ConvertFrom-Json
    if ($process.ExitCode -ne 0 -or $report.status -ne 'passed' -or $report.is_admin -ne $false) {
        throw 'Ordinary-account Launcher probe failed; see launcher.json and service logs'
    }
} finally {
    Remove-LocalUser -Name $account -ErrorAction SilentlyContinue
}
