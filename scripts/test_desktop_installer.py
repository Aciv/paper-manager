"""Installer path validation without starting maintenance or requiring services."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location(
    "desktop_installer", Path(__file__).resolve().parents[1] / "packaging/desktop/installer/install.py"
)
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


class DesktopInstallerTest(unittest.TestCase):
    def install_command(self, *options):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            program, data, shortcuts = root / "program", root / "data", root / "shortcuts"
            program.mkdir()
            (program / "installation.json").write_text(json.dumps({"active": "1.0.0-fixture"}), encoding="utf-8")
            argv = ["install.py", "install", "--no-launch", *options]
            with mock.patch.object(installer.sys, "argv", argv), \
                    mock.patch.object(installer, "default_paths", return_value=(program, data, shortcuts)), \
                    mock.patch.object(installer.subprocess, "run") as maintenance, \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(installer.main(), 0)
                command = maintenance.call_args.args[0]
            self.assertFalse(shortcuts.exists())
            return command, str(shortcuts.resolve())

    def test_shortcut_is_opt_in(self):
        command, _ = self.install_command()
        self.assertNotIn("--shortcut-dir", command)
        command, shortcuts = self.install_command("--create-shortcut")
        index = command.index("--shortcut-dir")
        self.assertEqual(command[index + 1], shortcuts)

    def test_explicit_shortcut_directory_is_also_opt_in(self):
        with tempfile.TemporaryDirectory() as temporary:
            shortcuts = Path(temporary) / "chosen"
            command, _ = self.install_command("--shortcut-dir", str(shortcuts))
            index = command.index("--shortcut-dir")
            self.assertEqual(command[index + 1], str(shortcuts.resolve()))

    def test_default_windows_install_never_starts_desktop_lookup(self):
        with mock.patch.object(installer.sys, "platform", "win32"), \
                mock.patch.dict(installer.os.environ, {"LOCALAPPDATA": "local", "APPDATA": "data"}), \
                mock.patch.object(installer.subprocess, "check_output") as lookup:
            _, _, shortcuts = installer.default_paths()
            self.assertIsNone(shortcuts)
            lookup.assert_not_called()

    def test_optional_windows_desktop_lookup_failure_is_nonfatal(self):
        with mock.patch.object(installer.sys, "platform", "win32"), \
                mock.patch.dict(installer.os.environ, {
                    "LOCALAPPDATA": "local", "APPDATA": "data", "SYSTEMROOT": "windows",
                }), \
                mock.patch.object(installer.subprocess, "check_output", side_effect=OSError("fixture")), \
                contextlib.redirect_stderr(io.StringIO()) as warnings:
            _, _, shortcuts = installer.default_paths(include_shortcut=True)
            self.assertIsNone(shortcuts)
            self.assertIn("shortcut skipped", warnings.getvalue())

    def test_windows_installer_accepts_ascii_paths_and_rejects_non_ascii(self):
        with mock.patch.object(installer.sys, "platform", "win32"):
            installer.validate_install_paths(Path("program"), Path("data"))
            for program, data in ((Path("程序"), Path("data")), (Path("program"), Path("数据"))):
                with self.subTest(program=program, data=data):
                    with self.assertRaisesRegex(ValueError, "--program-dir"):
                        installer.validate_install_paths(program, data)

    def test_windows_installer_rejects_path_before_running_maintenance(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            program, data = root / "程序", root / "data"
            argv = ["install.py", "install", "--program-dir", str(program), "--data-dir", str(data)]
            with mock.patch.object(installer.sys, "platform", "win32"), \
                    mock.patch.object(installer.sys, "argv", argv), \
                    mock.patch.object(installer, "default_paths", return_value=(program, data, root / "shortcuts")), \
                    mock.patch.object(installer.subprocess, "run") as maintenance:
                with self.assertRaisesRegex(ValueError, "Windows 安装目录"):
                    installer.main()
                maintenance.assert_not_called()
            self.assertFalse(program.exists())
            self.assertFalse(data.exists())

    def test_unix_installer_keeps_unicode_path_support(self):
        with mock.patch.object(installer.sys, "platform", "linux"):
            installer.validate_install_paths(Path("程序"), Path("数据"))


if __name__ == "__main__":
    unittest.main()
