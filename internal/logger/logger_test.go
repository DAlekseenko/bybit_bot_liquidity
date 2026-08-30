package logger

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewAppliesConfiguredLevel(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		wantDebug bool
		wantInfo  bool
	}{
		{name: "debug", level: "debug", wantDebug: true, wantInfo: true},
		{name: "info", level: "info", wantInfo: true},
		{name: "warn", level: "warn"},
		{name: "error", level: "error"},
		{name: "unknown defaults to info", level: "verbose", wantInfo: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			log := New(&output, test.level)
			log.Debug("debug-message")
			log.Info("info-message")

			got := output.String()
			if strings.Contains(got, "debug-message") != test.wantDebug {
				t.Fatalf("debug presence = %t, want %t; output=%q", strings.Contains(got, "debug-message"), test.wantDebug, got)
			}
			if strings.Contains(got, "info-message") != test.wantInfo {
				t.Fatalf("info presence = %t, want %t; output=%q", strings.Contains(got, "info-message"), test.wantInfo, got)
			}
		})
	}
}
