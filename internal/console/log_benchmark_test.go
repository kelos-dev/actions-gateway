package console

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func BenchmarkActionLogParser(b *testing.B) {
	for name, content := range map[string]string{
		"plain":      "Build completed successfully",
		"long_plain": strings.Repeat("build output ", 1000),
		"styled":     "\x1b[32mBuild completed\x1b[0m successfully",
	} {
		b.Run(name, func(b *testing.B) {
			line := "2026-08-10T12:34:56.123456789Z " + content
			parser := &actionLogParser{}
			b.SetBytes(int64(len(line)))
			b.ReportAllocs()
			for b.Loop() {
				parser.parse(line)
			}
		})
	}
}

func BenchmarkConsoleLogStream(b *testing.B) {
	for name, content := range map[string]string{
		"plain":  "Build completed successfully",
		"styled": "\x1b[32mBuild completed\x1b[0m successfully",
	} {
		b.Run(name, func(b *testing.B) {
			handler := newTestHandler(b, false)
			logs := strings.Repeat("2026-08-10T12:34:56.123456789Z "+content+"\n", 2000)
			handler.logs.(*testLogSource).logs = logs
			request := httptest.NewRequest(http.MethodGet, "/runs/default/ci/jobs/build/stream", nil)
			b.SetBytes(int64(len(logs)))
			b.ReportAllocs()
			for b.Loop() {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					b.Fatalf("log stream status = %d", response.Code)
				}
			}
		})
	}
}
