package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingWriter is a minimal http.ResponseWriter + http.Flusher that records
// exactly what a real client would have received.
type recordingWriter struct {
	header    http.Header
	status    int
	body      bytes.Buffer
	flushes   int
	headerSet bool
}

func newRecordingWriter() *recordingWriter {
	return &recordingWriter{header: make(http.Header)}
}

func (r *recordingWriter) Header() http.Header { return r.header }

func (r *recordingWriter) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *recordingWriter) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}

func (r *recordingWriter) Flush() { r.flushes++ }

func TestComboAttemptWriter_BuffersErrorUntilDiscard(t *testing.T) {
	dst := newRecordingWriter()
	aw := newComboAttemptWriter(dst)

	aw.WriteHeader(http.StatusBadGateway)
	if _, err := aw.Write([]byte(`{"error":"upstream exploded"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	if dst.status != 0 || dst.body.Len() != 0 {
		t.Fatalf("uncommitted error response leaked to client: status=%d body=%q", dst.status, dst.body.String())
	}
	if got := aw.Status(); got != http.StatusBadGateway {
		t.Fatalf("Status() = %d, want 502", got)
	}

	aw.Discard()
	aw.Permit()
	if dst.status != 0 || dst.body.Len() != 0 {
		t.Fatalf("discarded attempt reached client after late Permit: status=%d body=%q", dst.status, dst.body.String())
	}
}

func TestComboAttemptWriter_WriteHeaderErrorThenBodyStaysBuffered(t *testing.T) {
	dst := newRecordingWriter()
	aw := newComboAttemptWriter(dst)

	aw.WriteHeader(http.StatusServiceUnavailable)
	for i := 0; i < 10; i++ {
		if _, err := aw.Write([]byte("chunk")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if dst.body.Len() != 0 {
		t.Fatalf("body reached client before commit: %q", dst.body.String())
	}
	if aw.Committed() {
		t.Fatal("Committed() must stay false for a buffered error")
	}
}

func TestComboAttemptWriter_SuccessPermits(t *testing.T) {
	dst := newRecordingWriter()
	aw := newComboAttemptWriter(dst)

	aw.Header().Set("Content-Type", "application/json")
	aw.WriteHeader(http.StatusOK)
	if _, err := aw.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	if !aw.Committed() {
		t.Fatal("a 200 write must commit the attempt")
	}
	if dst.status != http.StatusOK || dst.body.String() != `{"ok":true}` {
		t.Fatalf("got status=%d body=%q", dst.status, dst.body.String())
	}
	if got := dst.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("shadow header was not applied on commit, got %q", got)
	}
}

func TestComboAttemptWriter_FlushPermits(t *testing.T) {
	dst := newRecordingWriter()
	aw := newComboAttemptWriter(dst)

	aw.WriteHeader(http.StatusOK)
	aw.Flush()
	if !aw.Committed() {
		t.Fatal("flushing a healthy attempt must commit it")
	}

	_, _ = aw.Write([]byte("data: {}\n\n"))
	aw.Flush()
	if dst.body.String() != "data: {}\n\n" {
		t.Fatalf("SSE payload did not pass through: %q", dst.body.String())
	}
	if dst.flushes < 2 {
		t.Fatalf("expected flushes to reach the client, got %d", dst.flushes)
	}
}

func TestComboAttemptWriter_DiscardDropsHeaders(t *testing.T) {
	dst := newRecordingWriter()

	// Attempt 1 writes an upstream-specific header, then fails and is dropped.
	failed := newComboAttemptWriter(dst)
	failed.Header().Set("X-Upstream-Request-Id", "abc123")
	failed.WriteHeader(http.StatusInternalServerError)
	failed.Discard()

	// Attempt 2 is a fresh writer, exactly as the attempt loop creates it.
	ok := newComboAttemptWriter(dst)
	ok.Header().Set("Content-Type", "text/event-stream")
	ok.WriteHeader(http.StatusOK)
	_, _ = ok.Write([]byte("data: ok\n\n"))

	if dst.header.Get("X-Upstream-Request-Id") != "" {
		t.Fatalf("discarded attempt leaked header to client: %v", dst.header)
	}
	if dst.header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("committed attempt did not apply its header: %v", dst.header)
	}
	if dst.status != http.StatusOK || dst.body.String() != "data: ok\n\n" {
		t.Fatalf("second attempt did not reach client: status=%d body=%q", dst.status, dst.body.String())
	}
}

func TestComboAttemptWriter_ErrorBodyIsCapped(t *testing.T) {
	dst := newRecordingWriter()
	aw := newComboAttemptWriter(dst)

	aw.WriteHeader(http.StatusInternalServerError)
	big := bytes.Repeat([]byte("x"), comboMaxErrorBuffer+4096)
	n, err := aw.Write(big)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len(big) {
		t.Fatalf("Write = %d, want %d: a capped write must still report success", n, len(big))
	}
	if got := aw.buf.Len(); got != comboMaxErrorBuffer {
		t.Fatalf("buffered %d bytes, want the %d cap", got, comboMaxErrorBuffer)
	}
	if dst.body.Len() != 0 {
		t.Fatalf("capped error body leaked to client: %d bytes", dst.body.Len())
	}
}

func TestComboAttemptWriter_CommittedReportsState(t *testing.T) {
	aw := newComboAttemptWriter(newRecordingWriter())

	if aw.Committed() {
		t.Fatal("a fresh attempt must not be committed")
	}
	aw.WriteHeader(http.StatusInternalServerError)
	if aw.Committed() {
		t.Fatal("an error status must not commit the attempt")
	}
	aw.Discard()
	if aw.Committed() {
		t.Fatal("a discarded attempt must not be committed")
	}

	ok := newComboAttemptWriter(newRecordingWriter())
	_, _ = ok.Write([]byte("hi"))
	if !ok.Committed() {
		t.Fatal("a write without an explicit status must commit as 200")
	}
}

func TestComboAttemptWriter_PermitAfterDiscardIsNoop(t *testing.T) {
	dst := newRecordingWriter()
	aw := newComboAttemptWriter(dst)

	aw.WriteHeader(http.StatusBadRequest)
	if _, err := aw.Write([]byte("bad")); err != nil {
		t.Fatalf("write: %v", err)
	}
	aw.Discard()
	aw.Permit()

	if dst.status != 0 || dst.body.Len() != 0 {
		t.Fatalf("Permit after Discard must not reach the client: status=%d body=%q", dst.status, dst.body.String())
	}
}

func TestComboAttemptWriter_ImplicitStatusIs200(t *testing.T) {
	dst := newRecordingWriter()
	aw := newComboAttemptWriter(dst)

	_, _ = aw.Write([]byte("hello"))
	if got := aw.Status(); got != http.StatusOK {
		t.Fatalf("Status() = %d, want 200 for an implicit success", got)
	}
	if dst.status != http.StatusOK {
		t.Fatalf("client status = %d, want 200", dst.status)
	}
}

// TestComboAttemptWriter_SatisfiesAdapterCommitDetection pins the interface the
// adapters probe to decide whether an error can still become a clean status.
func TestComboAttemptWriter_SatisfiesAdapterCommitDetection(t *testing.T) {
	var w http.ResponseWriter = newComboAttemptWriter(newRecordingWriter())

	if _, ok := w.(http.Flusher); !ok {
		t.Fatal("comboAttemptWriter must implement http.Flusher so SSE relays keep flushing")
	}
	type committer interface{ Committed() bool }
	if _, ok := w.(committer); !ok {
		t.Fatal("comboAttemptWriter must expose Committed() for the adapters' commit detection")
	}
}

// TestComboAttemptWriter_WorksAsRealResponseWriter drives it through httptest so
// the real net/http machinery sees the same behaviour.
func TestComboAttemptWriter_WorksAsRealResponseWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	aw := newComboAttemptWriter(rec)

	aw.Header().Set("Content-Type", "text/event-stream")
	aw.WriteHeader(http.StatusOK)
	if _, err := aw.Write([]byte("data: {\"delta\":\"hi\"}\n\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	aw.Flush()

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "delta") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Flushed == false {
		t.Fatal("flush did not reach the real writer")
	}
}
