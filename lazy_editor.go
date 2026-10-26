package main

import (
	"fmt"
	"sync"

	"titao/internal/psx"
)

// lazyEditor keeps startup and status checks free of editor processes.
type lazyEditor struct {
	mu       sync.Mutex
	initMu   sync.Mutex
	open     func() (editorSession, string, error)
	inner    editorSession
	starting editorSession
	version  string
	closed   bool
}

func (l *lazyEditor) get() (editorSession, error) {
	l.initMu.Lock()
	defer l.initMu.Unlock()
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, fmt.Errorf("套图引擎已关闭")
	}
	if l.inner != nil {
		s := l.inner
		l.mu.Unlock()
		return s, nil
	}
	s, _, err := l.open()
	if err != nil {
		l.mu.Unlock()
		return nil, err
	}
	l.starting = s
	l.mu.Unlock()
	v, err := s.Probe()
	l.mu.Lock()
	l.starting = nil
	if l.closed {
		l.mu.Unlock()
		return nil, fmt.Errorf("套图引擎已关闭")
	}
	if err != nil {
		l.mu.Unlock()
		s.Close()
		return nil, err
	}
	l.inner, l.version = s, v
	l.mu.Unlock()
	return s, nil
}

func (l *lazyEditor) Probe() (string, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.version, nil }

func (l *lazyEditor) Render(req psx.RenderRequest) (string, error) {
	s, err := l.get()
	if err != nil {
		return "", err
	}
	v, _ := l.Probe()
	req.PSVersion = v
	return s.Render(req)
}

func (l *lazyEditor) ProbeTemplate(path string) (*psx.TemplateInfo, error) {
	l.mu.Lock()
	s := l.inner
	l.mu.Unlock()
	if s == nil {
		return nil, fmt.Errorf("套图引擎尚未启动，首次启动任务后可探查模板")
	}
	return s.ProbeTemplate(path)
}

func (l *lazyEditor) CloseAllDocs() error {
	l.mu.Lock()
	s := l.inner
	l.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.CloseAllDocs()
}

func (l *lazyEditor) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	s := l.inner
	if s == nil {
		s = l.starting
	}
	l.inner = nil
	l.starting = nil
	l.version = ""
	l.mu.Unlock()
	if s != nil {
		return s.Close()
	}
	return nil
}
