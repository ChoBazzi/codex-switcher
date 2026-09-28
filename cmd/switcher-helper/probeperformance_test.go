package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Synthetic input only. Includes completed tool pairs and a fresh user boundary.
func probePerformanceBody(kind string) []byte {
	items := []any{map[string]any{"role": "user", "content": "synthetic first turn"}}
	count := 1000
	output := any(strings.Repeat("synthetic text ", 64))
	if kind == "image" {
		count = 1
		output = []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, 2<<20))}}
	}
	for i := 0; i < count; i++ {
		call := fmt.Sprintf("synthetic_call_%d", i)
		items = append(items,
			map[string]any{"type": "function_call", "call_id": call, "name": "read", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": call, "output": output},
			map[string]any{"role": "assistant", "content": "synthetic completed"})
	}
	items = append(items, map[string]any{"role": "user", "content": "synthetic next turn"})
	body, _ := json.Marshal(map[string]any{"input": items})
	return body
}

func benchmarkProbePreparation(body []byte) error {
	_ = sha256.Sum256(body)
	d := parseProbeToolInput(body)
	probeItemsUserBoundary(d.items)
	d.compactItems()
	_, err := d.normalize("a", "a", "synthetic-salt", nil, nil)
	_ = d.needsTurnOwner
	return err
}

func BenchmarkProbePreparation(b *testing.B) {
	for _, kind := range []string{"long_tools", "image"} {
		b.Run(kind, func(b *testing.B) {
			body := probePerformanceBody(kind)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := benchmarkProbePreparation(body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkProbeCheckpointCompare(b *testing.B) {
	a := &probeCheckpoint{Version: 1, Slot: "a"}
	for i := 0; i < 1024; i++ {
		a.Owners = append(a.Owners, probeCheckpointOwner{Key: sha256.Sum256([]byte(fmt.Sprint(i))), Slot: "a"})
	}
	c := *a
	c.Owners = nil
	owners := probeCompactRegistry{}
	for _, owner := range a.Owners {
		owners[owner.Key] = probeCompactOwner{owner.Slot, owner.Credential}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !sameProbeCheckpointRegistry(a, &c, owners) {
			b.Fatal("equal owner sets differ")
		}
	}
}
