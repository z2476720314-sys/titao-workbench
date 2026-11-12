package judge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTerraUsesNativeReasoningAndTokenParameters(t *testing.T) {
	body := map[string]any{"max_tokens": 4000, "temperature": .7}
	ApplyThinking(body, "gpt-6.1-terra", false)
	if body["reasoning_effort"] != "none" || body["max_completion_tokens"] != 4000 || body["max_tokens"] != nil || body["enable_thinking"] != nil {
		t.Fatalf("incorrect Terra parameters: %v", body)
	}
}

func TestStreamingVisionReplyIsReassembled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Error("request is not streaming")
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fmt.Fprint(w, ": keepalive\r\n\r\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"detail\"}}]}\r\n\r\n")
		for _, part := range []string{`{"pick":`, `1,"reason":"完整"}`} {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": part}}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"total_tokens\":12}}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	v := Choose(cfgFor(srv), cands(2), "pattern", Placement{})
	if v.Fallback || v.Pick != 0 || v.Reason != "完整" {
		t.Fatalf("stream reply not decoded: %+v", v)
	}
}

func TestStreamingUsageIsPreservedWithoutInventingMissingCounts(t *testing.T) {
	wire := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"completion_tokens\":12}}\n\ndata: [DONE]\n\n"
	b, err := readChatStream(strings.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Usage struct {
			Completion *int `json:"completion_tokens"`
			Prompt     *int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Usage.Completion == nil || *reply.Usage.Completion != 12 || reply.Usage.Prompt != nil {
		t.Fatalf("lost or manufactured usage: %s", b)
	}
}

func TestIncompleteOrFilteredStreamIsNotAcceptedOrReplayed(t *testing.T) {
	for _, ending := range []string{"", `data: [DONE]` + "\n\n", `data: {"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}` + "\n\ndata: [DONE]\n\n", `data: {"error":{"message":"private upstream detail"}}` + "\n\n"} {
		t.Run(fmt.Sprint(len(ending)), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"{\\\"pick\\\":1}\"}}]}\n\n"+ending)
			}))
			defer srv.Close()
			req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte(`{"stream":true}`)))
			resp, err := DoVisionRequest(req, time.Second)
			if resp != nil {
				resp.Body.Close()
			}
			if err == nil || calls != 1 || strings.Contains(fmt.Sprint(err), "private upstream detail") {
				t.Fatalf("incomplete stream accepted/replayed or leaked: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestStreamingTotalReplyRemainsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+strings.Repeat("x", (4<<20)+2))
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte(`{"stream":true}`)))
	resp, err := DoVisionRequest(req, time.Second)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("unbounded streaming reply: %v", err)
	}
}

func TestStreamingErrorEventOrEarlierFailureCannotBeOverwritten(t *testing.T) {
	for _, prefix := range []string{
		"event: error\ndata: {\"message\":\"upstream failed\"}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n",
	} {
		wire := prefix + "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"{\\\"pick\\\":1}\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		if _, err := readChatStream(strings.NewReader(wire)); err == nil {
			t.Fatal("earlier stream error was overwritten by a later stop")
		}
	}
}
