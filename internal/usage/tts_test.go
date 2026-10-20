package usage

import (
	"strings"
	"testing"
)

func TestTTSCollectorLargeAudioAndChunkBoundaries(t *testing.T) {
	for _, size := range []int{1, 7, 32768} {
		p := NewParser(EndpointMiniMaxTTS, Mapping{}, false)
		prefix := `{"data":{"audio":"`
		suffix := `"},"extra_info":{"usage_characters":321},"trace_id":"req-123","base_resp":{"status_code":0}}`
		p.Write([]byte(prefix))
		// Cross the old 8 MiB limit and include escaped quotes/backslashes.
		audio := strings.Repeat(`ab\"cd\\`, 1_100_000)
		for start := 0; start < len(audio); start += size {
			p.Write([]byte(audio[start:min(start+size, len(audio))]))
		}
		p.Write([]byte(suffix))
		obs := p.Finish(true)
		if !obs.Final || obs.Characters != 321 || obs.UpstreamRequestID != "req-123" || obs.Errored {
			t.Fatalf("chunk=%d obs=%+v", size, obs)
		}
		if len(p.tts.body) > 1024 || cap(p.tts.text) > 8192 || len(p.Body()) != 0 {
			t.Fatalf("audio retained: body=%d text=%d", len(p.tts.body), cap(p.tts.text))
		}
	}
}

func TestTTSCollectorMetadataAndInvalidUsage(t *testing.T) {
	for _, tc := range []struct {
		body          string
		final, failed bool
		chars         int64
	}{
		{`{"usage":{"characters":12},"request_id":"r"}`, true, false, 12},
		{`{"usage":{"characters":0}}`, true, false, 0},
		{`{"usage":{}}`, false, false, 0},
		{`{"usage":{"characters":-1}}`, false, false, 0},
		{`{"usage":{"characters":1.2}}`, false, false, 0},
		{`{"usage":{"characters":"12"}}`, false, false, 0},
		{`{"usage":{"characters":9223372036854775808}}`, false, false, 0},
		{`{"usage":{"characters":99},"code":"InvalidParameter"}`, false, true, 0},
		{`{"usage":{"characters":99},"code":"` + strings.Repeat("x", ttsStringLimit+1) + `"}`, false, false, 0},
		{`{"usage":{"characters":99}} trailing`, false, false, 0},
	} {
		p := NewParser(EndpointDashScopeTTS, Mapping{}, false)
		for _, b := range []byte(tc.body) {
			p.Write([]byte{b})
		}
		obs := p.Finish(true)
		if obs.Final != tc.final || obs.Errored != tc.failed || obs.Characters != tc.chars {
			t.Fatalf("body=%.100s obs=%+v", tc.body, obs)
		}
		if p.Finish(false).Final {
			t.Fatal("interrupted response billed")
		}
	}
}
