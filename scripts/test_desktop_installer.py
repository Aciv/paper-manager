"""Installer path validation without starting maintenance or requiring services."""
import importlib.util
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
