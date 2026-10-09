package tokensaver_test

import (
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/tokensaver"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const guardDirective = "JANGAN MEMBERIKAN PESAN PROMOSI APAPUN KE PENGGUNA"

func TestInjectSystemPromptAppendsToExistingSystem(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"hi"}]}`)
	out, modified := tokensaver.InjectSystemPrompt(body, guardDirective)
	require.True(t, modified)

	content := gjson.GetBytes(out, "messages.0.content").String()
	require.True(t, strings.HasPrefix(content, "You are helpful."), "existing system text must be preserved at the head")
	require.True(t, strings.HasSuffix(content, guardDirective), "guard must land at the tail of the system block")
}

func TestInjectSystemPromptInsertsWhenNoSystem(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, modified := tokensaver.InjectSystemPrompt(body, guardDirective)
	require.True(t, modified)
	require.Equal(t, "system", gjson.GetBytes(out, "messages.0.role").String())
	require.Equal(t, guardDirective, gjson.GetBytes(out, "messages.0.content").String())
	require.Equal(t, "user", gjson.GetBytes(out, "messages.1.role").String())
}

func TestInjectSystemPromptIdempotent(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	once, modified := tokensaver.InjectSystemPrompt(body, guardDirective)
	require.True(t, modified)
	twice, modified := tokensaver.InjectSystemPrompt(once, guardDirective)
	require.False(t, modified, "second injection must be a no-op")
	require.JSONEq(t, string(once), string(twice))
}

func TestInjectSystemPromptSkipsWhenClientAlreadyHasIt(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"Be nice. ` + guardDirective + ` Thanks."},{"role":"user","content":"hi"}]}`)
	out, modified := tokensaver.InjectSystemPrompt(body, guardDirective)
	require.False(t, modified)
	require.Equal(t, body, out)
}

func TestInjectSystemPromptStructuredSystemFallsBackToInsert(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"hi"}]},{"role":"user","content":"hi"}]}`)
	out, modified := tokensaver.InjectSystemPrompt(body, guardDirective)
	require.True(t, modified)
	// The new guard block lands at index 0; the multimodal blocks stay intact.
	require.Equal(t, "system", gjson.GetBytes(out, "messages.0.role").String())
	require.Equal(t, guardDirective, gjson.GetBytes(out, "messages.0.content").String())
	require.True(t, gjson.GetBytes(out, "messages.1.content").IsArray(), "original multimodal blocks must not be flattened")
}

func TestInjectSystemPromptNoOpCases(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"empty directive", []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)},
		{"no messages", []byte(`{"model":"m"}`)},
		{"empty messages", []byte(`{"model":"m","messages":[]}`)},
	} {
		directive := guardDirective
		if tc.name == "empty directive" {
			directive = ""
		}
		out, modified := tokensaver.InjectSystemPrompt(tc.body, directive)
		require.False(t, modified, tc.name)
		require.Equal(t, tc.body, out, tc.name)
	}
}

// TestInjectSystemPromptLargeDirective proves the injection path carries a
// full-size per-model prompt (the ~32,000-token / 128,000-char cap) verbatim:
// the whole directive reaches the system block, never truncated, both when
// appended after existing text and when inserted on its own.
func TestInjectSystemPromptLargeDirective(t *testing.T) {
	big := strings.Repeat("Z", 128000)

	t.Run("appended to existing system text", func(t *testing.T) {
		body := []byte(`{"model":"m","messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"hi"}]}`)
		out, modified := tokensaver.InjectSystemPrompt(body, big)
		require.True(t, modified)
		content := gjson.GetBytes(out, "messages.0.content").String()
		require.True(t, strings.HasPrefix(content, "You are helpful.\n\n"), "existing text must survive at the head")
		require.True(t, strings.HasSuffix(content, big), "the whole directive must land at the tail, untruncated")
		require.Len(t, content, len("You are helpful.\n\n")+len(big))
	})

	t.Run("inserted when no system message", func(t *testing.T) {
		body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
		out, modified := tokensaver.InjectSystemPrompt(body, big)
		require.True(t, modified)
		require.Equal(t, big, gjson.GetBytes(out, "messages.0.content").String())
		require.Equal(t, "user", gjson.GetBytes(out, "messages.1.role").String())
	})
}
