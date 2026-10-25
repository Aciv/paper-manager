package desktop

import (
	"os"
	"path/filepath"
	"runtime"
)

func (m *maintenance) writeUninstaller() error {
	var name, script string
	if runtime.GOOS == "windows" {
		name = "uninstall.ps1"
		script = "$ErrorActionPreference = 'Stop'\n" +
			"$record = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'installation.json') -Raw | ConvertFrom-Json\n" +
			"if ($record.active -notmatch '^[a-z0-9][a-z0-9.-]*$') { throw 'Invalid active version' }\n" +
			"$bundle = Join-Path (Join-Path $PSScriptRoot 'versions') $record.active\n" +
			"$temporary = Join-Path ([IO.Path]::GetTempPath()) ('paper-manager-uninstall-' + [guid]::NewGuid())\n" +
			"New-Item -ItemType Directory -Path $temporary | Out-Null\n" +
			"try {\n" +
			"  $helper = Join-Path $temporary 'launcher.exe'\n" +
			"  Copy-Item -LiteralPath (Join-Path $bundle 'launcher.exe') -Destination $helper\n" +
			"  & $helper --maintenance uninstall --data-dir " + powershellLiteral(m.root) +
			" --program-dir $PSScriptRoot\n  $code = $LASTEXITCODE\n" +
			"} finally { Remove-Item -LiteralPath $temporary -Recurse -Force }\nexit $code\n"
	} else {
		name = "uninstall"
		script = "#!/bin/sh\nset -eu\n" +
			"temporary=$(/usr/bin/mktemp -d \"${TMPDIR:-/tmp}/paper-manager-uninstall.XXXXXX\")\n" +
			"trap '/bin/rm -f \"$temporary/helper\"; /bin/rmdir \"$temporary\"' EXIT\n" +
			"/bin/cp " + shellLiteral(filepath.Join(m.versionPath(m.record.Active), "launcher")) + " \"$temporary/helper\"\n" +
			"\"$temporary/helper\" --maintenance uninstall --data-dir " + shellLiteral(m.root) +
			" --program-dir " + shellLiteral(m.program) + "\n"
	}
	f, err := os.CreateTemp(m.program, ".uninstall-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(script)
	if err == nil {
		err = f.Chmod(0700)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(m.program, name))
}
