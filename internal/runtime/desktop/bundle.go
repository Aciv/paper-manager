package desktop

import (
	"fmt"
	"os"
	"path/filepath"
)

// DeliveryDirectory is outside the sealed runtime so writable state and the
// operator's environment file survive removal of program components.
func DeliveryDirectory(bundle string) string {
	if filepath.Base(bundle) == "bundle" {
		return filepath.Dir(bundle)
	}
	return bundle
}

// DefaultBundleDataDir keeps portable deliveries self-contained. Standalone
// development launchers retain the original per-user default.
func DefaultBundleDataDir(bundle string) (string, error) {
	if info, err := os.Stat(filepath.Join(bundle, "bundle.json")); err == nil && !info.IsDir() {
		return filepath.Join(DeliveryDirectory(bundle), "data"), nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return DefaultDataDir()
}

// BundleDirectory supports both an unpacked delivery's root executable and
// the original launcher inside bundle/. It never depends on the current cwd.
func BundleDirectory(executable string) (string, error) {
	directory, err := filepath.Abs(filepath.Dir(executable))
	if err != nil {
		return "", err
	}
	for _, candidate := range []string{directory, filepath.Join(directory, "bundle")} {
		info, err := os.Stat(filepath.Join(candidate, "bundle.json"))
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	// Source development and standalone maintenance helpers have no manifest.
	return directory, nil
}

// OpenBundleConfig binds sealed deliveries to the chosen runtime. Persisted
// credentials and ports are retained; unpacking another runtime directory does
// not silently select its components for an existing database.
func OpenBundleConfig(root, bundle string) (Config, error) {
	info, err := os.Stat(filepath.Join(bundle, "bundle.json"))
	if os.IsNotExist(err) {
		return OpenConfig(root, bundle)
	}
	if err != nil {
		return Config{}, err
	}
	if info.IsDir() {
		return Config{}, fmt.Errorf("bundle.json must be a file")
	}
	var manifest bundleManifest
	if err := readRecord(filepath.Join(bundle, "bundle.json"), &manifest); err != nil {
		return Config{}, err
	}
	_, configErr := os.Stat(filepath.Join(root, "runtime.json"))
	fresh := os.IsNotExist(configErr)
	cfg, err := OpenConfig(root, bundle)
	if err != nil {
		return cfg, err
	}
	engine := cfg.ExtractionEngine
	if fresh && manifest.Profile == "rules" {
		engine = "rules"
	}
	selected, err := filepath.Abs(bundle)
	if err != nil {
		return cfg, err
	}
	expected := configForBundle(cfg, selected, engine)
	currentPaths := configForBundle(cfg, selected, cfg.ExtractionEngine)
	if sameRuntimeFiles(cfg, currentPaths) {
		// macOS may spell the same directory through /var and /private/var.
		// Keep persisted paths rather than treating this alias as a new build.
		expected.Server, expected.Python = cfg.Server, cfg.Python
		expected.ParserDir, expected.PostgresBin = cfg.ParserDir, cfg.PostgresBin
		expected.Redis, expected.WebDir = cfg.Redis, cfg.WebDir
		return expected, nil
	}
	old := filepath.Dir(cfg.Server)
	// Identical, intact copies may be switched without migrating user data.
	// A missing old bundle or a different build requires explicit maintenance.
	_, previousHash, previousErr := verifyBundle(old)
	_, selectedHash, selectedErr := verifyBundle(selected)
	if cfg == configForBundle(cfg, old, cfg.ExtractionEngine) &&
		previousErr == nil && selectedErr == nil && previousHash == selectedHash {
		return expected, nil
	}
	return cfg, fmt.Errorf("existing data uses another runtime directory; " +
		"open the original launcher or perform an explicit maintenance upgrade; data preserved")
}

func sameRuntimeFiles(a, b Config) bool {
	for _, pair := range [][2]*string{
		{&a.Server, &b.Server}, {&a.Python, &b.Python}, {&a.ParserDir, &b.ParserDir},
		{&a.PostgresBin, &b.PostgresBin}, {&a.Redis, &b.Redis}, {&a.WebDir, &b.WebDir},
	} {
		for _, path := range pair {
			if resolved, err := filepath.EvalSymlinks(*path); err == nil {
				*path = resolved
			}
		}
	}
	return a == b
}
