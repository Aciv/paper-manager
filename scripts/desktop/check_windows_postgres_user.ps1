# CI-only ordinary-account acceptance; never installs a PostgreSQL service.
$ErrorActionPreference = 'Stop'
if ($env:GITHUB_ACTIONS -ne 'true') { throw 'Run only in an ephemeral GitHub Actions Windows runner' }
$account = 'pm_probe_' + ([guid]::NewGuid().ToString('N').Substring(0, 8))
$password = ConvertTo-SecureString ('Pm!' + [guid]::NewGuid().ToString('N')) -AsPlainText -Force
$credential = [pscredential]::new("$env:COMPUTERNAME\$account", $password)
$root = Join-Path $env:PUBLIC $account
$output = Join-Path $env:GITHUB_WORKSPACE 'tmp/desktop-d0/postgres-ordinary-user.json'
$python = (Get-Command python).Source
$bin = Split-Path (Get-Command pg_ctl).Source
try {
    New-LocalUser -Name $account -Password $password -AccountNeverExpires | Out-Null
    # Well-known Users SID works on runners regardless of display language.
    if (-not (Get-LocalGroupMember -SID 'S-1-5-32-545' | Where-Object { $_.Name -eq "$env:COMPUTERNAME\$account" })) {
        Add-LocalGroupMember -SID 'S-1-5-32-545' -Member $account
    }
    New-Item -ItemType Directory -Path $root | Out-Null
    $acl = Get-Acl $root
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($sid in @('S-1-5-18', 'S-1-5-32-544')) {
        $identity = [System.Security.Principal.SecurityIdentifier]::new($sid)
        $rule = [System.Security.AccessControl.FileSystemAccessRule]::new($identity, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
        $acl.AddAccessRule($rule)
    }
    $rule = [System.Security.AccessControl.FileSystemAccessRule]::new("$env:COMPUTERNAME\$account", 'Modify', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
    $acl.AddAccessRule($rule)
    Set-Acl $root $acl
    Copy-Item 'scripts/desktop/check_private_postgres.py' (Join-Path $root 'probe.py')
    $arguments = @(('"' + (Join-Path $root 'probe.py') + '"'), '--bin-dir', ('"' + $bin + '"'),
                   '--output', ('"' + (Join-Path $root 'result.json') + '"'), '--temp-root', ('"' + $root + '"'))
    $process = Start-Process -FilePath $python -ArgumentList $arguments -Credential $credential -LoadUserProfile `
        -WorkingDirectory $root -Wait -PassThru `
        -RedirectStandardOutput (Join-Path $root 'stdout.log') -RedirectStandardError (Join-Path $root 'stderr.log')
    Copy-Item (Join-Path $root 'result.json') $output
    $report = Get-Content $output -Raw | ConvertFrom-Json
    if ($process.ExitCode -ne 0 -or $report.status -ne 'passed' -or $report.is_admin -ne $false) {
        throw "Ordinary-account PostgreSQL probe failed; see $output"
    }
    Write-Output 'PostgreSQL initialized, queried, restarted and stopped under a non-administrator account'
} finally {
    if (Test-Path $root) {
        Copy-Item (Join-Path $root '*.log') (Join-Path $env:GITHUB_WORKSPACE 'tmp/desktop-d0/') -ErrorAction SilentlyContinue
    }
    Remove-LocalUser -Name $account -ErrorAction SilentlyContinue
}
