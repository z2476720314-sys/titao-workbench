// Package psx 封装与 Adobe Photoshop 的 COM 交互。
//
// 关键约束：Photoshop 是单线程套间（STA）的自动化服务器，COM 代理必须在
// 同一个 OS 线程上创建和调用。Go 的 goroutine 会在系统线程间迁移，所以
// 整个会话固定在一个 runtime.LockOSThread 的专用线程上，外部调用通过
// channel 排队投递。
package psx

import (
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// PsDisplayNoDialogs：避免任何模态对话框。PS 一旦弹出模态窗口，
// 后续 COM 调用会永久挂起，这是实测踩过的坑。
const psDisplayNoDialogs = 3

// ErrClosed 表示会话已关闭。
var ErrClosed = errors.New("photoshop 会话已关闭")

// ErrNoPhotoshop 表示未能连接到 Photoshop（未安装或未运行）。
var ErrNoPhotoshop = errors.New("无法连接 Photoshop，请确认已安装并至少启动过一次")

// Conn 是 Photoshop Application COM 对象的封装。
//
// 只应在 Session 的专用线程上使用，不要在外部直接构造。
type Conn struct {
	app *ole.IDispatch
}

// Version 返回 Photoshop 版本号。
func (c *Conn) Version() (string, error) {
	v, err := oleutil.GetProperty(c.app, "Version")
	if err != nil {
		return "", fmt.Errorf("读取 PS 版本: %w", err)
	}
	defer v.Clear()
	return v.ToString(), nil
}

// DoJavaScript 执行 ExtendScript，返回脚本的字符串结果。
func (c *Conn) DoJavaScript(code string) (string, error) {
	v, err := oleutil.CallMethod(c.app, "DoJavaScript", code, nil, 1)
	if err != nil {
		return "", fmt.Errorf("执行 JSX: %w", err)
	}
	if v == nil {
		return "", nil
	}
	defer v.Clear()
	if v.VT == ole.VT_NULL || v.VT == ole.VT_EMPTY {
		return "", nil
	}
	return v.ToString(), nil
}

type call struct {
	fn   func(*Conn) error
	done chan error
}

// Session 是一个绑定到专用 OS 线程的 Photoshop 会话。
//
// 零值不可用，请用 Open 创建。Session 可被多个 goroutine 并发调用，
// 内部按投递顺序串行执行。
type Session struct {
	reqs  chan call
	ready chan struct{}
	quit  chan struct{}
	// exiting 在 loop 退出后关闭，用来告诉所有等待者「不会再有结果了」。
	// 只靠 quit 不够：quit 关闭后 loop 可能仍在执行一个长 JSX 调用，
	// 此时在途 call 的 done 永远不会收到值，Do 会永久阻塞。
	exiting chan struct{}

	mu     sync.Mutex
	closed bool

	conn *Conn
	err  error
}

// Open 创建会话并在后台线程上连接 Photoshop。
//
// 返回后即可调用 Do；若连接失败，Do 会返回该错误。
func Open() *Session {
	s := &Session{
		reqs:    make(chan call),
		ready:   make(chan struct{}),
		quit:    make(chan struct{}),
		exiting: make(chan struct{}),
	}
	go s.loop()
	return s
}

func (s *Session) loop() {
	// 锁定 OS 线程：整个生命周期内 goroutine 不会被调度到别的线程。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// 无论从哪条路径退出，都要唤醒所有在途等待者
	defer close(s.exiting)

	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		// RPC_E_CHANGED_MODE(0x80010106) 表示该线程已被初始化过，可以继续。
		var oleErr *ole.OleError
		if !errors.As(err, &oleErr) || uint32(oleErr.Code()) != 0x80010106 {
			s.finish(nil, fmt.Errorf("初始化 COM: %w", err))
			return
		}
	}
	defer ole.CoUninitialize()

	conn, err := dial()
	if err != nil {
		s.finish(nil, err)
		return
	}
	s.finish(conn, nil)

	for {
		select {
		case <-s.quit:
			if conn.app != nil {
				conn.app.Release()
			}
			return
		case c := <-s.reqs:
			c.done <- c.fn(conn)
		}
	}
}

func (s *Session) finish(conn *Conn, err error) {
	s.mu.Lock()
	s.conn, s.err = conn, err
	s.mu.Unlock()
	close(s.ready)
}

func dial() (*Conn, error) {
	unk, err := oleutil.CreateObject("Photoshop.Application")
	if err != nil {
		return nil, fmt.Errorf("%w（原始错误: %v）", ErrNoPhotoshop, err)
	}
	defer unk.Release()

	app, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, fmt.Errorf("获取 Photoshop 接口: %w", err)
	}

	// 全程禁掉对话框，避免模态窗口挂死 COM 调用。
	if _, err := oleutil.PutProperty(app, "DisplayDialogs", psDisplayNoDialogs); err != nil {
		app.Release()
		return nil, fmt.Errorf("设置 DisplayDialogs: %w", err)
	}
	return &Conn{app: app}, nil
}

// Do 在会话线程上执行 fn，阻塞直到完成。
//
// 三个返回路径都要能被关闭唤醒：连接失败、会话已关闭、loop 已退出。
// 否则一旦 PS 卡在长 JSX 里，调用方会永久阻塞。
func (s *Session) Do(fn func(*Conn) error) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}

	select {
	case <-s.ready:
	case <-s.exiting:
		return ErrClosed
	}
	if s.err != nil {
		return s.err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.mu.Unlock()

	c := call{fn: fn, done: make(chan error, 1)}
	select {
	case s.reqs <- c:
	case <-s.exiting:
		return ErrClosed
	}
	select {
	case err := <-c.done:
		return err
	case <-s.exiting:
		return ErrClosed
	}
}

// Close 结束会话并释放 COM 资源。
//
// 幂等。返回时 loop 已退出或正在退出；在途调用会收到 ErrClosed。
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	close(s.quit)
	return nil
}

// Probe 检查 Photoshop 是否可用，返回版本号。
func (s *Session) Probe() (string, error) {
	var ver string
	err := s.Do(func(c *Conn) error {
		v, err := c.Version()
		ver = v
		return err
	})
	return ver, err
}

// CloseAllDocs 关闭所有打开的文档（不保存）。
//
// 套图开始前调用，清掉上次残留的文档。
func (s *Session) CloseAllDocs() error {
	return s.Do(func(c *Conn) error {
		_, err := c.DoJavaScript(`(function(){
  for (var i = app.documents.length - 1; i >= 0; i--) {
    try { app.documents[i].close(SaveOptions.DONOTSAVECHANGES); } catch (e) {}
  }
  return "closed";
})()`)
		return err
	})
}
