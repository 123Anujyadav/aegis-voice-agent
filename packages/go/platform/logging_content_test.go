package platform

import (
	"log/slog"
	"reflect"
	"testing"
)

// ADR-0006 §10: prompts and transcripts are SENSITIVE and "neither is logged
// verbatim". These tests pin the content-key policy in logging.go
// (contentSubstrings, contentTokens).

const spokenContent = "my account number is twelve thirty four"

// TestRedaction_ContentKeysAreSuppressed covers both matching modes and the
// spellings a call site is likely to use.
func TestRedaction_ContentKeysAreSuppressed(t *testing.T) {
	t.Parallel()
	keys := []string{
		// substring mode
		"transcript", "partial_transcript", "TranscriptFinal", "utterance",
		"last_utterance", "prompt", "systemPrompt", "PROMPT_TEMPLATE",
		// token mode
		"text", "message_text", "message-text", "message.text", "messageText",
		"content", "reply_content", "replyContent", "body", "request_body",
		"RequestBody", "HTTPBody",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			log, buf := capture(t, "info")
			log.Info("turn", slog.String(key, spokenContent))
			if got := lastRecord(t, buf)[key]; got != redactedMarker {
				t.Errorf("%s = %v, want %s", key, got, redactedMarker)
			}
		})
	}
}

// TestRedaction_ContentTokensDoNotCatchLookalikes is why "text", "content"
// and "body" must be a key's last word: as substrings or as any word they
// would suppress these ordinary fields, which describe content rather than
// carry it.
func TestRedaction_ContentTokensDoNotCatchLookalikes(t *testing.T) {
	t.Parallel()
	keys := []string{
		"context", "context_id", "content_type", "contentType", "content_length",
		"body_size", "text_len", "textual_mode", "bodyguard", "antibody",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			log, buf := capture(t, "info")
			log.Info("turn", slog.String(key, "kept"))
			if got := lastRecord(t, buf)[key]; got != "kept" {
				t.Errorf("%s = %v, want it preserved", key, got)
			}
		})
	}
}

// TestRedaction_ContentInGroupsAndWithAttrs: content cannot be smuggled past
// the policy by nesting it in a group or attaching it to a derived logger.
func TestRedaction_ContentInGroupsAndWithAttrs(t *testing.T) {
	t.Parallel()
	log, buf := capture(t, "info")
	log.With(slog.String("transcript", spokenContent)).
		Info("turn", slog.Group("stt", slog.String("text", spokenContent)))

	rec := lastRecord(t, buf)
	if rec["transcript"] != redactedMarker {
		t.Errorf("transcript via With = %v, want %s", rec["transcript"], redactedMarker)
	}
	group, ok := rec["stt"].(map[string]any)
	if !ok {
		t.Fatalf("stt group missing: %v", rec)
	}
	if group["text"] != redactedMarker {
		t.Errorf("stt.text = %v, want %s", group["text"], redactedMarker)
	}
}

// TestRedaction_AllowedCorrelationKeysSurviveContentPolicy: the content
// policy runs after the exact-match allow list, like the rest.
func TestRedaction_AllowedCorrelationKeysSurviveContentPolicy(t *testing.T) {
	t.Parallel()
	log, buf := capture(t, "info")
	log.Info("turn", slog.String("correlation_id", "corr_0123456789ABCDEFGHJKMNPQRS"))
	if got := lastRecord(t, buf)["correlation_id"]; got != "corr_0123456789ABCDEFGHJKMNPQRS" {
		t.Errorf("correlation_id = %v, want it preserved", got)
	}
}

func TestKeyTokens(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"messageText":       {"message", "text"},
		"message_text":      {"message", "text"},
		"stt.final-Text":    {"stt", "final", "text"},
		"HTTPBody":          {"http", "body"},
		"userID2Text":       {"user", "id2", "text"},
		"request_id2":       {"request", "id2"},
		"__":                nil,
		"":                  nil,
		"content_type":      {"content", "type"},
		"partialTranscript": {"partial", "transcript"},
	}
	for in, want := range cases {
		if got := keyTokens(in); !reflect.DeepEqual(got, want) {
			t.Errorf("keyTokens(%q) = %q, want %q", in, got, want)
		}
	}
}
