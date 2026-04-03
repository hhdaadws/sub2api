package service

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/tidwall/gjson"
)

func testPrivacyConfig() *config.PrivacyConfig {
	return &config.PrivacyConfig{
		Enabled:            true,
		Platform:           "darwin",
		Shell:              "zsh",
		OSVersion:          "Darwin 24.4.0",
		WorkingDir:         "/Users/user/projects/myapp",
		HomeDir:            "/Users/user/",
		Email:              "user@example.com",
		NodeVersion:        "v22.13.0",
		Terminal:           "Apple_Terminal",
		Arch:               "arm64",
		CLIVersion:         "2.1.22",
		ConstrainedMemory:  17179869184,
		RSSMin:             100 * 1024 * 1024,
		RSSMax:             500 * 1024 * 1024,
		HeapTotalMin:       50 * 1024 * 1024,
		HeapTotalMax:       200 * 1024 * 1024,
		HeapUsedMin:        20 * 1024 * 1024,
		HeapUsedMax:        100 * 1024 * 1024,
		StripBillingHeader: true,
		RecomputeCCH:       true,
	}
}

func TestComputeCCH(t *testing.T) {
	// Verify the hash algorithm produces deterministic 3-char hex output
	hash := computeCCH("Hello, world! This is a test message.", "2.1.22")
	if len(hash) != 3 {
		t.Errorf("expected 3-char hash, got %q (len=%d)", hash, len(hash))
	}
	// Same input should produce same hash
	hash2 := computeCCH("Hello, world! This is a test message.", "2.1.22")
	if hash != hash2 {
		t.Errorf("CCH not deterministic: %q != %q", hash, hash2)
	}
	// Different version should produce different hash
	hash3 := computeCCH("Hello, world! This is a test message.", "2.2.0")
	if hash == hash3 {
		t.Errorf("Different versions produced same hash: %q", hash)
	}
}

func TestComputeCCH_ShortMessage(t *testing.T) {
	// Message shorter than position 20 should use '0' as fallback
	hash := computeCCH("Hi", "2.1.22")
	if len(hash) != 3 {
		t.Errorf("expected 3-char hash for short message, got %q", hash)
	}
}

func TestExtractFirstUserMessage(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "string content",
			body:     `{"messages":[{"role":"user","content":"hello world"}]}`,
			expected: "hello world",
		},
		{
			name:     "array content with text block",
			body:     `{"messages":[{"role":"user","content":[{"type":"text","text":"hello array"}]}]}`,
			expected: "hello array",
		},
		{
			name:     "skips assistant",
			body:     `{"messages":[{"role":"assistant","content":"hi"},{"role":"user","content":"user msg"}]}`,
			expected: "user msg",
		},
		{
			name:     "no messages",
			body:     `{"model":"claude-3"}`,
			expected: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFirstUserMessage([]byte(tt.body))
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestRewritePromptText_Platform(t *testing.T) {
	cfg := testPrivacyConfig()
	input := "Platform: linux\nShell: bash\nOS Version: Linux 6.18.5\nPrimary working directory: /home/dev/project\nHome: /home/dev/files"
	result := rewritePromptText(input, cfg, "")

	if !strings.Contains(result, "Platform: darwin") {
		t.Error("Platform not rewritten")
	}
	if !strings.Contains(result, "Shell: zsh") {
		t.Error("Shell not rewritten")
	}
	if !strings.Contains(result, "OS Version: Darwin 24.4.0") {
		t.Error("OS Version not rewritten")
	}
	if !strings.Contains(result, "Primary working directory: /Users/user/projects/myapp") {
		t.Error("Working directory not rewritten")
	}
	if strings.Contains(result, "/home/dev/") {
		t.Error("Home directory path not rewritten")
	}
}

func TestRewritePromptText_CCH(t *testing.T) {
	cfg := testPrivacyConfig()
	input := "some text cc_version=2.0.5.abc more text"
	result := rewritePromptText(input, cfg, "f1a")

	if !strings.Contains(result, "cc_version=2.1.22.f1a") {
		t.Errorf("CCH not rewritten: %s", result)
	}
}

func TestRewriteSystemReminders(t *testing.T) {
	cfg := testPrivacyConfig()
	input := `User said: hello
<system-reminder>
Platform: linux
Shell: bash
OS Version: Linux 6.18.5
Primary working directory: /home/dev/project
</system-reminder>
More user text`

	result := rewriteSystemReminders(input, cfg)

	// system-reminder content should be rewritten
	if !strings.Contains(result, "Platform: darwin") {
		t.Error("Platform in system-reminder not rewritten")
	}
	// Text outside system-reminder should be preserved
	if !strings.Contains(result, "User said: hello") {
		t.Error("User text outside system-reminder was modified")
	}
	if !strings.Contains(result, "More user text") {
		t.Error("User text after system-reminder was modified")
	}
}

func TestPrivacyRewriteSystemBody_StringSystem(t *testing.T) {
	cfg := testPrivacyConfig()
	body := []byte(`{"system":"Platform: linux\nShell: bash","messages":[{"role":"user","content":"hello world test msg abcdefghijklmnopqrstuvwxyz"}]}`)
	result, modified := PrivacyRewriteSystemBody(body, cfg)
	if !modified {
		t.Fatal("expected modification")
	}
	sys := gjson.GetBytes(result, "system").String()
	if !strings.Contains(sys, "Platform: darwin") {
		t.Error("Platform not rewritten in string system")
	}
}

func TestPrivacyRewriteSystemBody_ArraySystem(t *testing.T) {
	cfg := testPrivacyConfig()
	body := []byte(`{"system":[{"type":"text","text":"Platform: linux\nShell: bash"}],"messages":[{"role":"user","content":"hello world test msg abcdefghijklmnopqrstuvwxyz"}]}`)
	result, modified := PrivacyRewriteSystemBody(body, cfg)
	if !modified {
		t.Fatal("expected modification")
	}
	text := gjson.GetBytes(result, "system.0.text").String()
	if !strings.Contains(text, "Platform: darwin") {
		t.Error("Platform not rewritten in array system")
	}
}

func TestPrivacyRewriteSystemBody_StripBillingBlock(t *testing.T) {
	cfg := testPrivacyConfig()
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.0.5.abc"},{"type":"text","text":"You are Claude."}],"messages":[{"role":"user","content":"hi there test msg long enough for positions"}]}`)
	result, modified := PrivacyRewriteSystemBody(body, cfg)
	if !modified {
		t.Fatal("expected modification")
	}
	// Billing block should be removed
	sysArr := gjson.GetBytes(result, "system")
	if sysArr.IsArray() {
		count := 0
		sysArr.ForEach(func(_, item gjson.Result) bool {
			text := item.Get("text").String()
			if strings.Contains(text, "x-anthropic-billing-header") {
				t.Error("billing header block not stripped")
			}
			count++
			return true
		})
		if count != 1 {
			t.Errorf("expected 1 system block after stripping, got %d", count)
		}
	}
}

func TestPrivacyRewriteMessages(t *testing.T) {
	cfg := testPrivacyConfig()
	body := []byte(`{"messages":[{"role":"user","content":"hello <system-reminder>Platform: linux\nShell: bash</system-reminder> world"}]}`)
	result, modified := PrivacyRewriteMessages(body, cfg)
	if !modified {
		t.Fatal("expected modification")
	}
	content := gjson.GetBytes(result, "messages.0.content").String()
	if !strings.Contains(content, "Platform: darwin") {
		t.Error("Platform in message system-reminder not rewritten")
	}
	if !strings.Contains(content, "hello") || !strings.Contains(content, "world") {
		t.Error("User text around system-reminder was lost")
	}
}

func TestPrivacyRewriteMessages_ArrayContent(t *testing.T) {
	cfg := testPrivacyConfig()
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>Platform: linux</system-reminder>"}]}]}`)
	result, modified := PrivacyRewriteMessages(body, cfg)
	if !modified {
		t.Fatal("expected modification")
	}
	text := gjson.GetBytes(result, "messages.0.content.0.text").String()
	if !strings.Contains(text, "Platform: darwin") {
		t.Error("Platform in array message system-reminder not rewritten")
	}
}

func TestPrivacyStripBillingHeader(t *testing.T) {
	headers := map[string][]string{
		"Content-Type":               {"application/json"},
		"X-Anthropic-Billing-Header": {"cc_version=2.0.5.abc"},
		"Authorization":              {"Bearer token"},
	}
	cfg := testPrivacyConfig()
	PrivacyStripBillingHeader(headers, cfg)

	if _, ok := headers["X-Anthropic-Billing-Header"]; ok {
		t.Error("billing header not stripped")
	}
	if _, ok := headers["Content-Type"]; !ok {
		t.Error("Content-Type was incorrectly stripped")
	}
}

func TestPrivacyRewriteEventBatch(t *testing.T) {
	cfg := testPrivacyConfig()
	batch := map[string]any{
		"events": []any{
			map[string]any{
				"event_data": map[string]any{
					"device_id": "real_device_id_1234",
					"email":     "real@email.com",
					"env": map[string]any{
						"platform": "linux",
						"arch":     "x86_64",
						"terminal": "xterm",
					},
					"process": map[string]any{
						"constrainedMemory": 8589934592,
						"rss":               300000000,
						"heapTotal":         150000000,
						"heapUsed":          80000000,
					},
					"baseUrl": "http://localhost:8080",
					"gateway": true,
				},
			},
		},
	}
	body, _ := json.Marshal(batch)
	result := PrivacyRewriteEventBatch(body, cfg)

	// Check identity rewritten
	email := gjson.GetBytes(result, "events.0.event_data.email").String()
	if email != "user@example.com" {
		t.Errorf("email not rewritten: %s", email)
	}

	// Check env replaced
	platform := gjson.GetBytes(result, "events.0.event_data.env.platform").String()
	if platform != "darwin" {
		t.Errorf("env.platform not rewritten: %s", platform)
	}

	// Check leak fields stripped
	if gjson.GetBytes(result, "events.0.event_data.baseUrl").Exists() {
		t.Error("baseUrl not stripped")
	}
	if gjson.GetBytes(result, "events.0.event_data.gateway").Exists() {
		t.Error("gateway not stripped")
	}

	// Check process metrics randomized (should be within configured ranges)
	rss := gjson.GetBytes(result, "events.0.event_data.process.rss").Int()
	if rss < cfg.RSSMin || rss > cfg.RSSMax {
		t.Errorf("rss out of range: %d (expected %d-%d)", rss, cfg.RSSMin, cfg.RSSMax)
	}
	constMem := gjson.GetBytes(result, "events.0.event_data.process.constrainedMemory").Int()
	if constMem != cfg.ConstrainedMemory {
		t.Errorf("constrainedMemory not canonical: %d", constMem)
	}
}

func TestPrivacyRewriteEventBatch_Base64Process(t *testing.T) {
	cfg := testPrivacyConfig()
	procData := map[string]any{
		"constrainedMemory": 8589934592,
		"rss":               300000000,
		"heapTotal":         150000000,
		"heapUsed":          80000000,
	}
	procJSON, _ := json.Marshal(procData)
	procB64 := base64.StdEncoding.EncodeToString(procJSON)

	batch := map[string]any{
		"events": []any{
			map[string]any{
				"event_data": map[string]any{
					"device_id": "real_id",
					"process":   procB64,
				},
			},
		},
	}
	body, _ := json.Marshal(batch)
	result := PrivacyRewriteEventBatch(body, cfg)

	// Process should still be base64 encoded
	procResult := gjson.GetBytes(result, "events.0.event_data.process").String()
	decoded, err := base64.StdEncoding.DecodeString(procResult)
	if err != nil {
		t.Fatalf("process not base64 encoded after rewrite: %v", err)
	}
	var procMap map[string]any
	if err := json.Unmarshal(decoded, &procMap); err != nil {
		t.Fatalf("decoded process not valid JSON: %v", err)
	}
	if constMem, ok := procMap["constrainedMemory"].(float64); !ok || int64(constMem) != cfg.ConstrainedMemory {
		t.Errorf("constrainedMemory not canonical in base64 process: %v", procMap["constrainedMemory"])
	}
}

func TestPrivacyRewriteEventBatch_AdditionalMetadata(t *testing.T) {
	cfg := testPrivacyConfig()
	meta := map[string]any{
		"device_id": "leak_id",
		"baseUrl":   "http://leak.example.com",
		"other":     "preserved",
	}
	metaJSON, _ := json.Marshal(meta)
	metaB64 := base64.StdEncoding.EncodeToString(metaJSON)

	batch := map[string]any{
		"events": []any{
			map[string]any{
				"event_data": map[string]any{
					"additional_metadata": metaB64,
				},
			},
		},
	}
	body, _ := json.Marshal(batch)
	result := PrivacyRewriteEventBatch(body, cfg)

	metaResult := gjson.GetBytes(result, "events.0.event_data.additional_metadata").String()
	decoded, err := base64.StdEncoding.DecodeString(metaResult)
	if err != nil {
		t.Fatalf("additional_metadata not base64: %v", err)
	}
	var metaMap map[string]any
	if err := json.Unmarshal(decoded, &metaMap); err != nil {
		t.Fatalf("decoded metadata not valid JSON: %v", err)
	}
	if metaMap["device_id"] != "user@example.com" {
		t.Errorf("device_id in metadata not rewritten: %v", metaMap["device_id"])
	}
	if _, ok := metaMap["baseUrl"]; ok {
		t.Error("baseUrl not stripped from metadata")
	}
	if metaMap["other"] != "preserved" {
		t.Error("non-leak field was incorrectly removed from metadata")
	}
}

func TestPrivacyDisabled(t *testing.T) {
	cfg := &config.PrivacyConfig{Enabled: false}
	body := []byte(`{"system":"Platform: linux","messages":[{"role":"user","content":"test"}]}`)

	result, modified := PrivacyRewriteSystemBody(body, cfg)
	if modified {
		t.Error("should not modify when disabled")
	}
	if string(result) != string(body) {
		t.Error("body changed when privacy disabled")
	}

	result2, modified2 := PrivacyRewriteMessages(body, cfg)
	if modified2 {
		t.Error("messages should not be modified when disabled")
	}
	if string(result2) != string(body) {
		t.Error("messages changed when privacy disabled")
	}
}

func TestBuildCanonicalEnv(t *testing.T) {
	cfg := testPrivacyConfig()
	env := buildCanonicalEnv(cfg)

	if env["platform"] != "darwin" {
		t.Errorf("platform: %v", env["platform"])
	}
	if env["arch"] != "arm64" {
		t.Errorf("arch: %v", env["arch"])
	}
	if env["is_ci"] != false {
		t.Error("is_ci should be false")
	}
	if env["is_claude_ai_auth"] != true {
		t.Error("is_claude_ai_auth should be true")
	}
	if env["version"] != "2.1.22" {
		t.Errorf("version: %v", env["version"])
	}
}

func TestRandomInRange(t *testing.T) {
	min, max := int64(100), int64(200)
	for i := 0; i < 100; i++ {
		v := randomInRange(min, max)
		if v < min || v >= max {
			t.Errorf("randomInRange(%d, %d) = %d out of range", min, max, v)
		}
	}
	// Edge case: min == max
	v := randomInRange(100, 100)
	if v != 100 {
		t.Errorf("randomInRange(100, 100) = %d, want 100", v)
	}
}
