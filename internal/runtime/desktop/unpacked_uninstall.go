package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
)

// AcquireBundle prevents removing a runtime used by any Launcher instance,
// including instances with a custom data directory. Locking an existing file
// keeps sealed/read-only bundles free of additional state files.
func AcquireBundle(bundle string) (*InstanceLock, error) {
	name := "launcher"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	f, err := os.Open(filepath.Join(bundle, name))
	if err != nil {
		return nil, err
	}
	lock, err := lockBundleFile(f)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(bundle), ".uninstalling")); !os.IsNotExist(err) {
		lock.Close()
		return nil, fmt.Errorf("program is being uninstalled")
	}
	return lock, nil
}

// RemoveUnpacked removes only an intact, recognized application's program
// files. User data and unrelated files beside the program are preserved.
func RemoveUnpacked(program string) error {
	program, err := filepath.Abs(program)
	if err != nil {
		return err
	}
	bundle := filepath.Join(program, "bundle")
	lock, err := AcquireBundle(bundle)
	if err != nil {
		return fmt.Errorf("exit Launcher before uninstalling: %w", err)
	}
	manifest, _, err := verifyBundle(bundle)
	if err != nil {
		lock.Close()
		return fmt.Errorf("program files changed; remove the program directory manually; data preserved: %w", err)
	}
	removeSkills := unmodifiedDeliverySkills(program, manifest)
	entry, script := "paper_manager", "Uninstall.sh"
	if runtime.GOOS == "windows" {
		entry, script = "paper_manager.exe", "Uninstall.cmd"
	} else if runtime.GOOS == "darwin" {
		script = "Uninstall.command"
	}
	native := "launcher"
	if runtime.GOOS == "windows" {
		native += ".exe"
	}
	a, errA := fileHash(filepath.Join(program, entry))
	b, errB := fileHash(filepath.Join(bundle, native))
	if errA != nil || errB != nil || a != b {
		lock.Close()
		return fmt.Errorf("unrecognized program directory; data preserved")
	}
	// Publish the removal marker while holding the admission lock. Every new
	// Launcher checks this marker after acquiring that same lock.
	retired := bundle + ".uninstalling"
	if _, err := os.Lstat(retired); !os.IsNotExist(err) {
		lock.Close()
		return fmt.Errorf("previous uninstall directory exists; data preserved")
	}
	marker := filepath.Join(program, ".uninstalling")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		lock.Close()
		return err
	}
	lock.Close()
	if err := os.Rename(bundle, retired); err != nil {
		_ = os.Remove(marker)
		return err
	}
	if err := os.RemoveAll(retired); err != nil {
		return fmt.Errorf("remove stopped program files: %w", err)
	}
	if removeSkills {
		if err := os.RemoveAll(filepath.Join(program, "skills")); err != nil {
			return err
		}
	}
	for _, name := range []string{entry, script, "README.md", ".uninstalling"} {
		if err := os.Remove(filepath.Join(program, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	// Leave any unrelated files and the directory containing them untouched.
	_ = os.Remove(program)
	return nil
}

// Only the exact shipped copy is disposable. Edited skill instructions and
// newly added files belong to the user and survive program removal.
func unmodifiedDeliverySkills(program string, manifest bundleManifest) bool {
	path := filepath.Join(program, "skills")
	if !realChildDirectory(path) {
		return false
	}
	expected := map[string]bundleFile{}
	for name, item := range manifest.Files {
		if strings.HasPrefix(name, "skills/") {
			expected[strings.TrimPrefix(name, "skills/")] = item
		}
	}
	if len(expected) == 0 {
		return false
	}
	observed, err := fileInventory(path, false)
	return err == nil && reflect.DeepEqual(expected, observed)
}
