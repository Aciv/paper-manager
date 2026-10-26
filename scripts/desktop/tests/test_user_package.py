"""User archives contain only platform-specific runtime and removal entries."""
import json
import os
from pathlib import Path
import tempfile
import unittest

from desktop_bundle import extract, inventory, sha256, write_json
from check_desktop_package import verify_delivery
import test_desktop
from user_delivery import package_user


class UserPackageTests(unittest.TestCase):
    def test_user_archives_are_small_surfaces_and_tamper_checked(self):
        for target, entry, script, suffix in [
            ("linux-x86_64", "paper_manager", "Uninstall.sh", ".tar.gz"),
            ("macos-arm64", "paper_manager", "Uninstall.command", ".tar.gz"),
            ("windows-x86_64", "paper_manager.exe", "Uninstall.cmd", ".zip"),
        ]:
            with self.subTest(target=target), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                bundle = test_desktop.DeliveryTests().bundle(root, target)
                for name in ("papers-manager-library", "papers-manager-reading"):
                    skill = bundle / "skills" / name
                    (skill / "references").mkdir(parents=True)
                    (skill / "SKILL.md").write_text("skill entry")
                    (skill / "references/tools.md").write_text("tool reference")
                manifest = json.loads((bundle / "bundle.json").read_text())
                manifest["unpacked_uninstall"] = 1
                manifest["files"] = inventory(bundle)
                write_json(bundle / "bundle.json", manifest)
                checks = [f"{item['sha256']}  {name}" for name, item in manifest["files"].items()]
                checks.append(f"{sha256(bundle / 'bundle.json')}  bundle.json")
                (bundle / "SHA256SUMS").write_text("\n".join(checks) + "\n")
                archive = package_user(bundle, root / ("desktop" + suffix))
                destination = root / "user"
                extract(archive, destination)
                external = archive.with_name(archive.name + ".manifest.json")
                verify_delivery(destination, external)
                self.assertEqual(
                    {path.name for path in destination.iterdir()},
                    {entry, script, "bundle", "skills", "README.md", ".launcher_env"},
                )
                self.assertTrue((destination / "skills/papers-manager-reading/references/tools.md").is_file())
                if os.name != "nt":
                    self.assertTrue(os.access(destination / script, os.X_OK))
                skill_path = destination / "skills/papers-manager-reading/SKILL.md"
                skill_path.write_text("modified instruction")
                with self.assertRaisesRegex(ValueError, "skills differ"):
                    verify_delivery(destination, external)
                skill_path.write_text("skill entry")
                (destination / script).write_text("modified")
                with self.assertRaises(ValueError):
                    verify_delivery(destination, external)

    def test_old_launcher_cannot_ship_a_nonfunctional_uninstaller(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            bundle = test_desktop.DeliveryTests().bundle(root, "linux-x86_64")
            with self.assertRaisesRegex(ValueError, "rebuild"):
                package_user(bundle, root / "desktop.tar.gz")
            self.assertFalse((root / "desktop.tar.gz").exists())


if __name__ == "__main__":
    unittest.main()
