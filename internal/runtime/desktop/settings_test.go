package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func settingsRuntime(t *testing.T) (*Runtime, Config) {
	t.Helper()
	cfg, err := OpenConfig(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg)
	r.EnableConfigReload()
	return r, cfg
}

func TestStoppedSettingsPersistAndRejectRunningOrInvalidWrites(t *testing.T) {
	r, cfg := settingsRuntime(t)
	settings := r.Settings().Settings
	settings.Concurrency = Concurrency{16, 2, 2, 2, 4}
	if err := r.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	saved, err := OpenConfig(cfg.DataDir, filepath.Dir(cfg.Server))
	if err != nil || saved.Concurrency != settings.Concurrency || saved.DatabasePassword != cfg.DatabasePassword {
		t.Fatalf("settings not persisted with original identity: %v", err)
	}
	if !strings.Contains(strings.Join(r.apiEnv(), "\n"), "ASYNQ_CONCURRENCY=16") {
		t.Fatal("task limit did not reach API environment")
	}
	args := strings.Join(r.parserArgs(), " ")
	if !strings.Contains(args, "--recognize-concurrency 2") || !strings.Contains(args, "--execution-mode shared") {
		t.Fatal("parser admission or shared execution mode changed")
	}
	path := filepath.Join(cfg.DataDir, "runtime.json")
	before, _ := os.ReadFile(path)
	bad := settings
	bad.Ports.API = bad.Ports.Redis
	if err := r.UpdateSettings(bad); err == nil {
		t.Fatal("duplicate ports accepted")
	}
	r.cycleDone = make(chan struct{})
	r.state.State = "starting"
	if err := r.UpdateSettings(settings); !errors.Is(err, errConfigBusy) {
		t.Fatalf("running write accepted: %v", err)
	}
	if err := r.ReloadConfiguration(); !errors.Is(err, errConfigBusy) {
		t.Fatal("running JSON reload accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected write changed configuration")
	}
	r.finishCycle(r.cycleDone, "stopped", "")
	if !r.Snapshot().CanConfigure {
		t.Fatal("terminal state did not release configuration admission")
	}
}

func TestConfigurationFileReloadDefaultsAndRestartBoundaries(t *testing.T) {
	r, cfg := settingsRuntime(t)
	path := filepath.Join(cfg.DataDir, "runtime.json")
	data, _ := os.ReadFile(path)
	var legacy map[string]any
	_ = json.Unmarshal(data, &legacy)
	delete(legacy, "concurrency")
	data, _ = json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.ReloadConfiguration(); err != nil || r.Settings().Concurrency.RPCs != 8 {
		t.Fatalf("old config rejected: %v", err)
	}
	cfg.Concurrency.Extraction = 1
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := r.ReloadConfiguration(); err != nil || r.Settings().Concurrency.Extraction != 1 {
		t.Fatalf("file edit not applied: %v", err)
	}
	cfg.DatabasePassword = secret()
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := r.ReloadConfiguration(); err == nil {
		t.Fatal("changed runtime identity applied while manager was running")
	}
	if err := os.WriteFile(path, []byte("broken JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if r.Start() || r.Snapshot().State != "failed" {
		t.Fatal("invalid JSON did not stop service admission")
	}
}

func TestConfigurationHTTPIsSecretFreeAndOriginBounded(t *testing.T) {
	r, cfg := settingsRuntime(t)
	handler := r.Handler()
	call := func(method, path, origin string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, r.ManagementURL()+path, bytes.NewReader(body))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	response := call(http.MethodGet, "/configuration", "", nil)
	if response.Code != 200 || strings.Contains(response.Body.String(), cfg.DatabasePassword) {
		t.Fatal("configuration form leaked identity or failed")
	}
	settings := r.Settings().Settings
	settings.Concurrency.Tasks = 12
	data, _ := json.Marshal(settings)
	if call(http.MethodPut, "/configuration", "https://untrusted.example", data).Code != 403 {
		t.Fatal("cross-origin settings write accepted")
	}
	if call(http.MethodPut, "/configuration", r.ManagementURL(), data).Code != 200 {
		t.Fatal("stopped configuration save failed")
	}
	if call(http.MethodGet, "/configuration/file", "", nil).Code != 200 {
		t.Fatal("explicit JSON file link failed")
	}
}

func TestAPIAdvertisesCurrentWebPort(t *testing.T) {
	r, _ := settingsRuntime(t)
	for _, port := range []int{28453, 39453} {
		r.cfg.Ports.API = port
		want := "WEB_BASE_URL=http://" + address(port) + "/"
		found := false
		for _, entry := range r.apiEnv() {
			if entry == want {
				found = true
			}
		}
		if !found {
			t.Fatal("current API Web address missing from child environment")
		}
	}
}
