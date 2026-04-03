package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const billingHeaderName = "x-anthropic-billing-header"

// ── CCH hash algorithm (reverse-engineered from Claude Code CLI) ──

const cchSalt = "59cf53e54c78"

var cchPositions = [3]int{4, 7, 20}

// computeCCH computes the Claude Code billing hash from the first user message.
func computeCCH(firstUserMessage, version string) string {
	var chars strings.Builder
	for _, pos := range cchPositions {
		if pos < len(firstUserMessage) {
			chars.WriteByte(firstUserMessage[pos])
		} else {
			chars.WriteByte('0')
		}
	}
	h := sha256.Sum256([]byte(cchSalt + chars.String() + version))
	return hex.EncodeToString(h[:])[:3]
}

// extractFirstUserMessage extracts the text of the first user message from the
// API request body (Claude /v1/messages format).
func extractFirstUserMessage(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	var result string
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			result = content.String()
			return false
		}
		if content.IsArray() {
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "text" {
					result = block.Get("text").String()
					return false
				}
				return true
			})
			if result != "" {
				return false
			}
		}
		return true
	})
	return result
}

// ── Precompiled regex patterns for prompt rewriting ──

var (
	platformRegex   = regexp.MustCompile(`(Platform:\s*)\S+`)
	shellRegex      = regexp.MustCompile(`(Shell:\s*)\S+`)
	osVersionRegex  = regexp.MustCompile(`(OS Version:\s*)[^\n<]+`)
	workingDirRegex = regexp.MustCompile(`((?:Primary )?[Ww]orking directory:\s*)/\S+`)
	homeDirRegex    = regexp.MustCompile(`/(?:Users|home)/[^/\s]+/`)
	ccVersionRegex  = regexp.MustCompile(`cc_version=[\d.]+\.[a-f0-9]{3}`)
	sysReminderRe   = regexp.MustCompile(`(<system-reminder>)([\s\S]*?)(</system-reminder>)`)
	billingBlockRe  = regexp.MustCompile(`(?m)^\s*` + billingHeaderName + `:[^\n]*\n?`)
)

// rewritePromptText rewrites environment fields and billing hash inside a
// system prompt or system-reminder block.
// If cchHash is empty, billing hash rewriting is skipped.
func rewritePromptText(text string, cfg *config.PrivacyConfig, cchHash string) string {
	result := text

	// 1. Billing header hash (only when hash is available)
	if cchHash != "" && cfg.RecomputeCCH {
		result = ccVersionRegex.ReplaceAllString(result,
			fmt.Sprintf("cc_version=%s.%s", cfg.CLIVersion, cchHash))
	}

	// 2. Platform / Shell / OS Version
	result = platformRegex.ReplaceAllString(result, "${1}"+cfg.Platform)
	result = shellRegex.ReplaceAllString(result, "${1}"+cfg.Shell)
	result = osVersionRegex.ReplaceAllString(result, "${1}"+cfg.OSVersion)

	// 3. Working directory
	result = workingDirRegex.ReplaceAllString(result, "${1}"+cfg.WorkingDir)

	// 4. Home directory paths
	result = homeDirRegex.ReplaceAllString(result, cfg.HomeDir)

	return result
}

// rewriteSystemReminders rewrites only <system-reminder> blocks within message
// text. These are injected by Claude Code (environment info, git status, etc.)
// and are not user-authored content. cchHash rewriting is skipped inside
// message-level system-reminders (hash is applied in system prompt only).
func rewriteSystemReminders(text string, cfg *config.PrivacyConfig) string {
	if !strings.Contains(text, "<system-reminder>") {
		return text
	}
	return sysReminderRe.ReplaceAllStringFunc(text, func(match string) string {
		parts := sysReminderRe.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		return parts[1] + rewritePromptText(parts[2], cfg, "") + parts[3]
	})
}

// ── Public API ──

// PrivacyRewriteRequestBody applies all privacy rewrites to a request body:
// system prompt rewriting, billing header stripping, and message system-reminder
// normalization. Returns the (possibly modified) body.
func PrivacyRewriteRequestBody(body []byte, cfg *config.PrivacyConfig) []byte {
	if cfg == nil || !cfg.Enabled {
		return body
	}
	if next, changed := PrivacyRewriteSystemBody(body, cfg); changed {
		body = next
	}
	if next, changed := PrivacyRewriteMessages(body, cfg); changed {
		body = next
	}
	return body
}

// PrivacyRewriteSystemBody rewrites system prompt blocks for privacy:
//   - Strips billing header blocks (x-anthropic-billing-header text blocks)
//   - Rewrites environment fields (Platform, Shell, OS, paths)
//   - Recomputes CCH billing hash
//
// Returns the modified body and whether any changes were made.
func PrivacyRewriteSystemBody(body []byte, cfg *config.PrivacyConfig) ([]byte, bool) {
	if cfg == nil || !cfg.Enabled {
		return body, false
	}

	out := body
	modified := false

	// Compute CCH hash from first user message
	var cchHash string
	if cfg.RecomputeCCH {
		firstMsg := extractFirstUserMessage(body)
		if firstMsg != "" {
			cchHash = computeCCH(firstMsg, cfg.CLIVersion)
		}
	}

	sys := gjson.GetBytes(body, "system")
	if !sys.Exists() {
		return body, false
	}

	switch {
	case sys.Type == gjson.String:
		text := sys.String()
		// Strip inline billing header
		if cfg.StripBillingHeader {
			text = billingBlockRe.ReplaceAllString(text, "")
		}
		rewritten := rewritePromptText(text, cfg, cchHash)
		if rewritten != sys.String() {
			if next, err := sjson.SetBytes(out, "system", rewritten); err == nil {
				out = next
				modified = true
			}
		}

	case sys.IsArray():
		index := 0
		toRemove := []int{}
		sys.ForEach(func(_, item gjson.Result) bool {
			// Check for billing header block
			if cfg.StripBillingHeader {
				text := ""
				if item.Type == gjson.String {
					text = item.String()
				} else {
					text = item.Get("text").String()
				}
				if strings.TrimSpace(text) != "" && billingBlockRe.MatchString(text) &&
					strings.HasPrefix(strings.TrimSpace(text), billingHeaderName) {
					toRemove = append(toRemove, index)
					index++
					return true
				}
			}

			// Rewrite text fields
			if item.Get("type").String() == "text" || item.Type == gjson.String {
				var text string
				var path string
				if item.Type == gjson.String {
					text = item.String()
					path = fmt.Sprintf("system.%d", index)
				} else {
					text = item.Get("text").String()
					path = fmt.Sprintf("system.%d.text", index)
				}
				rewritten := rewritePromptText(text, cfg, cchHash)
				if rewritten != text {
					if next, err := sjson.SetBytes(out, path, rewritten); err == nil {
						out = next
						modified = true
					}
				}
			}
			index++
			return true
		})

		// Remove billing header blocks (reverse order to preserve indices)
		for i := len(toRemove) - 1; i >= 0; i-- {
			path := fmt.Sprintf("system.%d", toRemove[i])
			if next, err := sjson.DeleteBytes(out, path); err == nil {
				out = next
				modified = true
			}
		}
	}

	return out, modified
}

// PrivacyRewriteMessages rewrites <system-reminder> blocks within message
// content for privacy normalization.
func PrivacyRewriteMessages(body []byte, cfg *config.PrivacyConfig) ([]byte, bool) {
	if cfg == nil || !cfg.Enabled {
		return body, false
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body, false
	}

	out := body
	modified := false
	msgIndex := 0
	messages.ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if content.Type == gjson.String {
			text := content.String()
			rewritten := rewriteSystemReminders(text, cfg)
			if rewritten != text {
				path := fmt.Sprintf("messages.%d.content", msgIndex)
				if next, err := sjson.SetBytes(out, path, rewritten); err == nil {
					out = next
					modified = true
				}
			}
		} else if content.IsArray() {
			blockIndex := 0
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "text" {
					text := block.Get("text").String()
					rewritten := rewriteSystemReminders(text, cfg)
					if rewritten != text {
						path := fmt.Sprintf("messages.%d.content.%d.text", msgIndex, blockIndex)
						if next, err := sjson.SetBytes(out, path, rewritten); err == nil {
							out = next
							modified = true
						}
					}
				}
				blockIndex++
				return true
			})
		}
		msgIndex++
		return true
	})

	return out, modified
}

// PrivacyStripBillingHeader removes x-anthropic-billing-header from HTTP
// request headers if configured.
func PrivacyStripBillingHeader(header map[string][]string, cfg *config.PrivacyConfig) {
	if cfg == nil || !cfg.Enabled || !cfg.StripBillingHeader {
		return
	}
	for k := range header {
		if strings.EqualFold(k, billingHeaderName) {
			delete(header, k)
		}
	}
}

// ── Telemetry (event_logging/batch) rewriting ──

// PrivacyRewriteEventBatch rewrites a /api/event_logging/batch payload,
// normalizing identity, environment, and process fields.
func PrivacyRewriteEventBatch(body []byte, cfg *config.PrivacyConfig) []byte {
	if cfg == nil || !cfg.Enabled {
		return body
	}

	events := gjson.GetBytes(body, "events")
	if !events.IsArray() {
		return body
	}

	out := body
	eventIdx := 0
	events.ForEach(func(_, event gjson.Result) bool {
		data := event.Get("event_data")
		if !data.Exists() {
			eventIdx++
			return true
		}
		prefix := fmt.Sprintf("events.%d.event_data", eventIdx)

		// Identity fields
		if data.Get("device_id").Exists() {
			if next, err := sjson.SetBytes(out, prefix+".device_id", cfg.Email); err == nil {
				out = next
			}
		}
		if data.Get("email").Exists() {
			if next, err := sjson.SetBytes(out, prefix+".email", cfg.Email); err == nil {
				out = next
			}
		}

		// Environment fingerprint - replace entire env object
		if data.Get("env").Exists() {
			canonicalEnv := buildCanonicalEnv(cfg)
			if envJSON, err := json.Marshal(canonicalEnv); err == nil {
				if next, err := sjson.SetRawBytes(out, prefix+".env", envJSON); err == nil {
					out = next
				}
			}
		}

		// Process metrics - randomize
		if data.Get("process").Exists() {
			out = rewriteProcessMetrics(out, prefix+".process", data.Get("process"), cfg)
		}

		// Strip leak fields
		for _, field := range []string{"baseUrl", "base_url", "gateway"} {
			if data.Get(field).Exists() {
				if next, err := sjson.DeleteBytes(out, prefix+"."+field); err == nil {
					out = next
				}
			}
		}

		// additional_metadata - decode base64, rewrite, re-encode
		if data.Get("additional_metadata").Exists() {
			out = rewriteAdditionalMetadata(out, prefix+".additional_metadata",
				data.Get("additional_metadata"), cfg)
		}

		eventIdx++
		return true
	})

	return out
}

func buildCanonicalEnv(cfg *config.PrivacyConfig) map[string]any {
	return map[string]any{
		"platform":               cfg.Platform,
		"platform_raw":           cfg.Platform,
		"arch":                   cfg.Arch,
		"node_version":           cfg.NodeVersion,
		"terminal":               cfg.Terminal,
		"package_managers":       "npm,yarn",
		"runtimes":               "node,python",
		"is_running_with_bun":    false,
		"is_ci":                  false,
		"is_claubbit":            false,
		"is_claude_code_remote":  false,
		"is_local_agent_mode":    false,
		"is_conductor":           false,
		"is_github_action":       false,
		"is_claude_code_action":  false,
		"is_claude_ai_auth":      true,
		"version":                cfg.CLIVersion,
		"version_base":           cfg.CLIVersion,
		"build_time":             "2025-01-01T00:00:00.000Z",
		"deployment_environment": "production",
		"vcs":                    "git",
	}
}

func rewriteProcessMetrics(body []byte, path string, proc gjson.Result, cfg *config.PrivacyConfig) []byte {
	out := body

	if proc.Type == gjson.String {
		// base64-encoded process data
		decoded, err := base64.StdEncoding.DecodeString(proc.String())
		if err != nil {
			return body
		}
		var procMap map[string]any
		if err := json.Unmarshal(decoded, &procMap); err != nil {
			return body
		}
		randomizeProcessFields(procMap, cfg)
		reEncoded, err := json.Marshal(procMap)
		if err != nil {
			return body
		}
		encoded := base64.StdEncoding.EncodeToString(reEncoded)
		if next, err := sjson.SetBytes(out, path, encoded); err == nil {
			return next
		}
		return body
	}

	// JSON object
	if proc.IsObject() {
		canonical := map[string]any{
			"constrainedMemory": cfg.ConstrainedMemory,
			"rss":               randomInRange(cfg.RSSMin, cfg.RSSMax),
			"heapTotal":         randomInRange(cfg.HeapTotalMin, cfg.HeapTotalMax),
			"heapUsed":          randomInRange(cfg.HeapUsedMin, cfg.HeapUsedMax),
		}
		if procJSON, err := json.Marshal(canonical); err == nil {
			if next, err := sjson.SetRawBytes(out, path, procJSON); err == nil {
				return next
			}
		}
	}

	return body
}

func randomizeProcessFields(m map[string]any, cfg *config.PrivacyConfig) {
	m["constrainedMemory"] = cfg.ConstrainedMemory
	m["rss"] = randomInRange(cfg.RSSMin, cfg.RSSMax)
	m["heapTotal"] = randomInRange(cfg.HeapTotalMin, cfg.HeapTotalMax)
	m["heapUsed"] = randomInRange(cfg.HeapUsedMin, cfg.HeapUsedMax)
}

func randomInRange(min, max int64) int64 {
	if min >= max {
		return min
	}
	return min + rand.Int63n(max-min)
}

func rewriteAdditionalMetadata(body []byte, path string, val gjson.Result, cfg *config.PrivacyConfig) []byte {
	if val.Type != gjson.String {
		return body
	}
	decoded, err := base64.StdEncoding.DecodeString(val.String())
	if err != nil {
		return body
	}

	var meta map[string]any
	if err := json.Unmarshal(decoded, &meta); err != nil {
		return body
	}

	// Strip leak fields
	delete(meta, "baseUrl")
	delete(meta, "base_url")
	delete(meta, "gateway")

	// Rewrite identity
	if _, ok := meta["device_id"]; ok {
		meta["device_id"] = cfg.Email
	}
	if _, ok := meta["email"]; ok {
		meta["email"] = cfg.Email
	}

	reEncoded, err := json.Marshal(meta)
	if err != nil {
		return body
	}
	encoded := base64.StdEncoding.EncodeToString(reEncoded)
	if next, err := sjson.SetBytes(body, path, encoded); err == nil {
		return next
	}
	return body
}
