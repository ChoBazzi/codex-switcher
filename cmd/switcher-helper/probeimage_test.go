package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Synthetic 1x1 PNG only; never copy a user's screenshot into a fixture.
const probeSyntheticPNGURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jq6cAAAAASUVORK5CYII="

func probeImageHistory(kind, output, suffix string) []byte {
	field := "arguments"
	if kind == "custom_tool_call" {
		field = "input"
	}
	return []byte(`{"input":[{"role":"user","content":"inspect"},{"type":"` + kind + `","id":"synthetic-item","call_id":"synthetic-call","name":"inspect","` + field + `":"{}"},{"type":"` + kind + `_output","id":"synthetic-result","call_id":"synthetic-call","output":` + output + `}` + suffix + `]}`)
}

func TestProbeToolsInlineImageHistory(t *testing.T) {
	output := `[{"type":"input_text","text":"Synthetic image result."},{"type":"input_image","image_url":"` + probeSyntheticPNGURL + `","detail":"high"}]`
	for _, kind := range []string{"function_call", "custom_tool_call"} {
		t.Run(kind, func(t *testing.T) {
			current := probeImageHistory(kind, output, "")
			if _, err := probeToolBody(current, "a", "a", "synthetic"); err != nil {
				t.Fatal("same-account image tool result rejected")
			}
			if _, err := probeToolBody(current, "b", "a", "synthetic"); err == nil {
				t.Fatal("image bypassed current tool turn ownership")
			}
			past := probeImageHistory(kind, output, `,{"role":"user","content":"continue"}`)
			var before, after struct{ Input []map[string]json.RawMessage }
			_ = json.Unmarshal(past, &before)
			portable, err := probeToolBody(past, "b", "a", "synthetic")
			if err != nil || json.Unmarshal(portable, &after) != nil {
				t.Fatal("past image history blocked the next user request")
			}
			var original, forwarded any
			_ = json.Unmarshal(before.Input[2]["output"], &original)
			_ = json.Unmarshal(after.Input[2]["output"], &forwarded)
			if !reflect.DeepEqual(original, forwarded) {
				t.Fatal("image, detail, text, or ordering changed")
			}
			if strings.Contains(string(portable), "synthetic-item") || strings.Contains(string(portable), "synthetic-result") || strings.Contains(string(portable), "synthetic-call") {
				t.Fatal("source server identifiers escaped")
			}
			if probeString(after.Input[1], "call_id") == "" || probeString(after.Input[1], "call_id") != probeString(after.Input[2], "call_id") {
				t.Fatal("image result lost its tool pair")
			}
			beforeBoundary, beforeOK := probeUserBoundary(past)
			afterBoundary, afterOK := probeUserBoundary(portable)
			if !beforeOK || !afterOK || beforeBoundary != afterBoundary {
				t.Fatal("image changed the user boundary")
			}
		})
	}
}

func TestProbeToolsInlineImageFormats(t *testing.T) {
	// Encoding/type validation is local; raster validity remains upstream's job.
	for _, mime := range []string{"png", "jpeg", "webp", "gif"} {
		for _, detail := range []string{"", "auto", "low", "high", "original"} {
			t.Run(mime+"/"+detail, func(t *testing.T) {
				part := map[string]any{"type": "input_image", "image_url": "data:image/" + mime + ";base64,c3ludGhldGlj"}
				if detail != "" {
					part["detail"] = detail
				}
				output, _ := json.Marshal([]any{part})
				if _, err := probeToolBody(probeImageHistory("function_call", string(output), ""), "a", "a", "synthetic"); err != nil {
					t.Fatal("supported inline image encoding rejected")
				}
			})
		}
	}
	// Exercise valid streaming chunks with no padding, one '=', and two '='.
	for _, size := range []int{1536, 1537, 1538} {
		encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, size))
		output, _ := json.Marshal([]any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + encoded}})
		if _, err := probeToolBody(probeImageHistory("custom_tool_call", string(output), ""), "a", "a", "synthetic"); err != nil {
			t.Fatal("valid multi-chunk base64 rejected")
		}
	}
}

func TestProbeToolsInlineImageRejectsReferencesAndInvalidShape(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"missing_url", "image_url", ""},
		{"null_url", "image_url", `null`},
		{"object_url", "image_url", `{}`},
		{"remote", "image_url", `"https://synthetic.invalid/private.png"`},
		{"local_file", "image_url", `"file:///synthetic-private.png"`},
		{"relative_file", "image_url", `"synthetic-private.png"`},
		{"file_id", "file_id", `"synthetic-private"`},
		{"opaque", "encrypted_content", `"synthetic-private"`},
		{"unknown_field", "synthetic-private", `true`},
		{"detail_null", "detail", `null`},
		{"detail_number", "detail", `1`},
		{"detail_unknown", "detail", `"synthetic-private"`},
		{"empty", "image_url", `"data:image/png;base64,"`},
		{"bad_base64", "image_url", `"data:image/png;base64,%%%"`},
		{"trailing_bits", "image_url", `"data:image/png;base64,Zh=="`},
		{"unclosed_base64", "image_url", `"data:image/png;base64,Zg"`},
		{"base64_space", "image_url", `"data:image/png;base64,Z g=="`},
		{"base64_newline", "image_url", `"data:image/png;base64,Zg==\n"`},
		{"interior_padding_chunk_boundary", "image_url", `"data:image/png;base64,` + strings.Repeat("A", 1020) + `Zg==AAAA"`},
		{"interior_single_padding_chunk_boundary", "image_url", `"data:image/png;base64,` + strings.Repeat("A", 1020) + `Zm8=AAAA"`},
		{"svg", "image_url", `"data:image/svg+xml;base64,c3ludGhldGlj"`},
		{"html", "image_url", `"data:text/html;base64,c3ludGhldGlj"`},
		{"parameters", "image_url", `"data:image/png;name=private;base64,c3ludGhldGlj"`},
		{"not_base64", "image_url", `"data:image/png,synthetic-private"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			part := map[string]json.RawMessage{"type": json.RawMessage(`"input_image"`)}
			part["image_url"], _ = json.Marshal(probeSyntheticPNGURL)
			if tc.value == "" {
				delete(part, tc.key)
			} else {
				part[tc.key] = json.RawMessage(tc.value)
			}
			output, _ := json.Marshal([]any{part})
			_, err := probeToolBody(probeImageHistory("custom_tool_call", string(output), ""), "a", "a", "synthetic")
			var failure *probeBodyError
			if !errors.As(err, &failure) || failure.Reason != "tool_output_invalid" {
				t.Fatal("unsafe image was not rejected with a fixed code")
			}
			diagnostic, _ := json.Marshal(failure)
			if strings.Contains(string(diagnostic), "synthetic-private") || strings.Contains(string(diagnostic), "base64") {
				t.Fatal("image or reference leaked into diagnostics")
			}
		})
	}
}

func TestProbeToolsInlineImageScopeAndLimit(t *testing.T) {
	part := `{"type":"input_image","image_url":"` + probeSyntheticPNGURL + `","detail":"high"}`
	// Standalone user/agent image inputs and unpaired outputs remain unsupported.
	for _, body := range []string{
		`{"input":[{"role":"user","content":[` + part + `]}]}`,
		`{"input":[{"role":"user","content":"inspect"},{"type":"agent_message","author":"child","recipient":"root","content":[` + part + `]}]}`,
		`{"input":[{"role":"user","content":"inspect"},{"type":"custom_tool_call_output","call_id":"missing","output":[` + part + `]}]}`,
	} {
		if _, err := probeToolBody([]byte(body), "a", "a", "synthetic"); err == nil {
			t.Fatal("image support bypassed existing history restrictions")
		}
	}
	oversized, _ := json.Marshal([]any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", 4<<20)}})
	if _, err := probeToolBody(probeImageHistory("function_call", string(oversized), ""), "a", "a", "synthetic"); err == nil {
		t.Fatal("oversized inline image accepted")
	}
}

func TestProbeToolsInlineImageDispatch(t *testing.T) {
	for _, route := range []string{"root", "auxiliary"} {
		for _, source := range []string{"inline", "remote", "invalid"} {
			t.Run(route+"/"+source, func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					body, _ := io.ReadAll(r.Body)
					if !strings.Contains(string(body), probeSyntheticPNGURL) {
						t.Error("inline image missing from upstream request")
					}
					var payload struct{ Input []map[string]json.RawMessage }
					_ = json.Unmarshal(body, &payload)
					detailPreserved := false
					for _, item := range payload.Input {
						var parts []map[string]json.RawMessage
						_ = json.Unmarshal(item["output"], &parts)
						for _, part := range parts {
							if probeString(part, "type") == "input_image" && probeString(part, "detail") == "high" {
								detailPreserved = true
							}
						}
					}
					if !detailPreserved {
						t.Error("inline image detail was not preserved at dispatch")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[]}]}}\n\n")
				}))
				defer upstream.Close()
				var send func([]byte) int
				if route == "root" {
					dir := filepath.Join(t.TempDir(), "runtime")
					if err := privateServiceDir(dir); err != nil {
						t.Fatal("private test directory failed")
					}
					p := startCheckpointProcess(t, dir, upstream.URL)
					defer p.stop()
					home := p.next(t, "probe_ready")["codex_home"].(string)
					address, secret, _ := checkpointProfile(t, home)
					send = func(body []byte) int { return checkpointRequest(t, address, secret, string(body)) }
				} else {
					var mu sync.Mutex
					a := &probeAuxiliary{binding: probeAuxiliaryBinding{Thread: "synthetic-child", Root: "synthetic-root", Slot: "a"}}
					defer func() {
						if a.handler != nil {
							a.handler.Close()
						}
					}()
					send = func(body []byte) int {
						w := httptest.NewRecorder()
						r := httptest.NewRequest("POST", "/responses", bytes.NewReader(body))
						a.serve(w, r, &mu, syntheticProbeAccess{}, upstream.URL, "synthetic", func() bool { return true }, nil)
						return w.Code
					}
				}
				url := probeSyntheticPNGURL
				if source == "remote" {
					url = "https://synthetic.invalid/private.png"
				} else if source == "invalid" {
					url = "data:image/png;base64,%%%"
				}
				output, _ := json.Marshal([]any{map[string]any{"type": "input_image", "image_url": url, "detail": "high"}})
				body := probeImageHistory("custom_tool_call", string(output), `,{"role":"user","content":"continue"}`)
				status := send(body)
				if source == "inline" {
					if status != 200 || calls.Load() != 1 {
						t.Fatal("valid image did not make exactly one upstream request")
					}
				} else {
					if status != 409 || calls.Load() != 0 {
						t.Fatal("invalid or referenced image reached upstream")
					}
					if send(body) != 409 || calls.Load() != 0 {
						t.Fatal("failed image request was replayed")
					}
				}
			})
		}
	}
}
