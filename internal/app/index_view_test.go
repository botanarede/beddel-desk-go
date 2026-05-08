package app

import (
	"strings"
	"testing"

	"github.com/botanarede/beddel-desk-go/internal/embedding/download"
)

// TestHumanBytes covers the tag-free formatter that both the
// disclosure message and the download progress line use.
func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{-1, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{int64(90) * 1024 * 1024, "90.0 MiB"},
		{int64(5) * 1024 * 1024 * 1024, "5.0 GiB"},
	}
	for _, tc := range cases {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDisclosureMessageMentionsEveryAsset verifies the modal body
// includes all three manifest entries so the user sees the full set
// of downloads before confirming.
func TestDisclosureMessageMentionsEveryAsset(t *testing.T) {
	msg := disclosureMessage(download.DefaultManifest)
	for _, needle := range []string{"ONNX", "all-MiniLM-L6-v2", "tokenizer"} {
		if !strings.Contains(msg, needle) {
			t.Errorf("disclosure message missing %q: %q", needle, msg)
		}
	}
	// The text must also mention SHA-256 so the user understands the
	// integrity check is automatic.
	if !strings.Contains(msg, "SHA-256") {
		t.Errorf("disclosure message missing SHA-256 reference: %q", msg)
	}
}

// TestDisclosureMessageHandlesEmptyManifest verifies the formatter
// does not panic when the runtime matrix is empty (a defensive check
// since the string is user-facing).
func TestDisclosureMessageHandlesEmptyManifest(t *testing.T) {
	m := download.Manifest{
		Runtimes:  map[download.PlatformKey]download.Entry{},
		Model:     download.Entry{Version: "v1", Size: 1},
		Tokenizer: download.Entry{Version: "v1", Size: 1},
	}
	msg := disclosureMessage(m)
	if msg == "" {
		t.Fatalf("disclosure message is empty for empty runtime matrix")
	}
}

// TestFormatDownloadProgressStages covers the user-visible text for
// each documented download stage. Reusing the constants from the
// download package would couple the test too tightly to internal
// names; we assert on the visible wording instead.
func TestFormatDownloadProgressStages(t *testing.T) {
	cases := []struct {
		stage   string
		current int64
		total   int64
		asset   string
		want    []string // substrings that must appear in the output
	}{
		{"probing", 0, 0, "", []string{"Checking system"}},
		{"downloading", 2048, 1024 * 1024, "onnxruntime", []string{"Downloading", "onnxruntime", "2.0 KiB", "1.0 MiB"}},
		{"downloading", 2048, 0, "model", []string{"Downloading", "model", "2.0 KiB"}},
		{"verifying", 0, 0, "model", []string{"Verifying", "model"}},
		{"ready", 0, 0, "", []string{"ready"}},
		{"unknown-stage", 0, 0, "foo", []string{"unknown-stage", "foo"}},
	}
	for _, tc := range cases {
		p := download.Progress{
			Stage:     tc.stage,
			Current:   tc.current,
			Total:     tc.total,
			AssetName: tc.asset,
		}
		got := formatDownloadProgress(p)
		for _, needle := range tc.want {
			if !strings.Contains(got, needle) {
				t.Errorf("stage %q: got %q, missing %q", tc.stage, got, needle)
			}
		}
	}
}
