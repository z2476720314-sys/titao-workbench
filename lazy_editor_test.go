package main

import (
	"sync"
	"testing"
	"time"

	"titao/internal/psx"
)

type stubEditor struct{ renders, closes int }

func (s *stubEditor) Probe() (string, error) { return "test-version", nil }
func (s *stubEditor) ProbeTemplate(string) (*psx.TemplateInfo, error) {
	return &psx.TemplateInfo{}, nil
}

type blockingEditor struct {
	stubEditor
	started, release chan struct{}
	once             sync.Once
}

func (s *blockingEditor) Probe() (string, error) { close(s.started); <-s.release; return "ready", nil }
func (s *blockingEditor) Close() error           { s.once.Do(func() { close(s.release) }); return nil }

func TestBlockedInitializationDoesNotBlockStatusOrClose(t *testing.T) {
	s := &blockingEditor{started: make(chan struct{}), release: make(chan struct{})}
	defer s.Close()
	l := &lazyEditor{open: func() (editorSession, string, error) { return s, "test", nil }}
	go l.Render(psx.RenderRequest{})
	<-s.started
	status := make(chan struct{})
	go func() { l.Probe(); close(status) }()
	select {
	case <-status:
	case <-time.After(time.Second):
		t.Error("status blocked by initialization")
	}
	closed := make(chan struct{})
	go func() { l.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Error("close blocked by initialization")
	}
}

func TestClosedEditorCannotStartLater(t *testing.T) {
	opens := 0
	l := &lazyEditor{open: func() (editorSession, string, error) { opens++; return &stubEditor{}, "test", nil }}
	l.Close()
	if _, err := l.Render(psx.RenderRequest{}); err == nil || opens != 0 {
		t.Fatalf("opened after close: opens=%d err=%v", opens, err)
	}
}
func (s *stubEditor) Render(psx.RenderRequest) (string, error) { s.renders++; return "ok", nil }
func (s *stubEditor) CloseAllDocs() error                      { return nil }
func (s *stubEditor) Close() error                             { s.closes++; return nil }

func TestEditorStartsOnlyWhenRendering(t *testing.T) {
	opens := 0
	inner := &stubEditor{}
	l := &lazyEditor{open: func() (editorSession, string, error) { opens++; return inner, "test", nil }}
	if v, err := l.Probe(); err != nil || v != "" {
		t.Fatalf("idle probe: %q %v", v, err)
	}
	l.CloseAllDocs()
	if opens != 0 {
		t.Fatal("startup/status probe opened editor")
	}
	for i := 0; i < 2; i++ {
		if _, err := l.Render(psx.RenderRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	if opens != 1 || inner.renders != 2 {
		t.Fatalf("opens=%d renders=%d", opens, inner.renders)
	}
	l.Close()
	if inner.closes != 1 {
		t.Fatal("session not released")
	}
}
