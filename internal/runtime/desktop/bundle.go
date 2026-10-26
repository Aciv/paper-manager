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
	if _, err := os.Stat(filepath.Join(directory, "bundle")); err == nil {
		return "", fmt.Errorf("bundle/ 缺少 bundle.json，请完整解压新版程序")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	// Source development and standalone maintenance helpers have no manifest.
	return directory, nil
}

// OpenBundleConfig uses the selected sealed runtime with existing portable data.
// Credentials, ports and user settings survive replacement of program files;
// persisted component paths and fingerprints never select an older runtime.
func OpenBundleConfig(root, bundle string) (Config, error) {
	if _, err := os.Stat(filepath.Join(bundle, "bundle.json")); os.IsNotExist(err) {
		cfg, configErr := OpenConfig(root, bundle)
		if configErr == nil && cfg.BundleHash != "" {
			return cfg, fmt.Errorf("当前运行包缺少 bundle.json，请完整解压新版；原有数据已保留")
		}
		return cfg, configErr
	} else if err != nil {
		return Config{}, err
	}
	selected, err := filepath.Abs(bundle)
	if err != nil {
		return Config{}, err
	}
	// Validate the current package and its platform/data/queue compatibility
	// before creating or replacing any configuration. The old package need not
	// exist: users can copy all of data/ or replace the stopped program files.
	manifest, digest, err := verifyBundle(selected)
	if err != nil {
		return Config{}, fmt.Errorf("当前运行包不完整或不兼容，原有数据已保留: %w", err)
	}
	_, configErr := os.Stat(filepath.Join(root, "runtime.json"))
	fresh := os.IsNotExist(configErr)
	cfg, err := OpenConfig(root, selected)
	if err != nil {
		return cfg, err
	}
	engine := cfg.ExtractionEngine
	if fresh && manifest.Profile == "rules" {
		engine = "rules"
	}
	expected := configForBundle(cfg, selected, engine)
	expected.BundleHash = digest
	if sameRuntimeFiles(cfg, expected) {
		// Preserve equivalent spellings such as macOS /var and /private/var.
		expected.Server, expected.Python = cfg.Server, cfg.Python
		expected.ParserDir, expected.PostgresBin = cfg.ParserDir, cfg.PostgresBin
		expected.Redis, expected.WebDir = cfg.Redis, cfg.WebDir
	}
	return expected, nil
}

func sameRuntimeFiles(a, b Config) bool {
	b.BundleHash = a.BundleHash
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
