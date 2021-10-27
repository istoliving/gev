package gev

import (
	"errors"
	"runtime"
	"time"

	"github.com/Allenxuxu/gev/log"
	"github.com/Allenxuxu/toolkit/sync"
	"github.com/Allenxuxu/toolkit/sync/atomic"
	"github.com/RussellLuo/timingwheel"
	"golang.org/x/sys/unix"
)

// Handler Server 注册接口
type Handler interface {
	CallBack
	OnConnect(c *Connection)
}

// Server gev Server
type Server struct {
	loop      *eventLoop
	workLoops []*eventLoop
	callback  Handler

	timingWheel *timingwheel.TimingWheel
	opts        *Options
	running     atomic.Bool
}

// NewServer 创建 Server
func NewServer(handler Handler, opts ...Option) (server *Server, err error) {
	if handler == nil {
		return nil, errors.New("handler is nil")
	}
	options := newOptions(opts...)
	server = new(Server)
	server.callback = handler
	server.opts = options
	server.timingWheel = timingwheel.NewTimingWheel(server.opts.tick, server.opts.wheelSize)
	server.loop, err = newEventLoop()
	if err != nil {
		_ = server.loop.stop()
		return nil, err
	}

	l, err := newListener(server.opts.Network, server.opts.Address, options.ReusePort, server.loop, server.handleNewConnection)
	if err != nil {
		return nil, err
	}
	if err = server.loop.addSocketAndEnableRead(l.fd, l); err != nil {
		return nil, err
	}

	if server.opts.NumLoops <= 0 {
		server.opts.NumLoops = runtime.NumCPU()
	}

	wloops := make([]*eventLoop, server.opts.NumLoops)
	for i := 0; i < server.opts.NumLoops; i++ {
		l, err := newEventLoop()
		if err != nil {
			for j := 0; j < i; j++ {
				_ = wloops[j].stop()
			}
			return nil, err
		}
		wloops[i] = l
	}
	server.workLoops = wloops

	return
}

// RunAfter 延时任务
func (s *Server) RunAfter(d time.Duration, f func()) *timingwheel.Timer {
	return s.timingWheel.AfterFunc(d, f)
}

// RunEvery 定时任务
func (s *Server) RunEvery(d time.Duration, f func()) *timingwheel.Timer {
	return s.timingWheel.ScheduleFunc(&everyScheduler{Interval: d}, f)
}

func (s *Server) handleNewConnection(fd int, sa unix.Sockaddr) {
	loop := s.opts.Strategy(s.workLoops)

	c := NewConnection(fd, loop, sa, s.opts.Protocol, s.timingWheel, s.opts.IdleTime, s.callback)

	loop.queueInLoop(func() {
		s.callback.OnConnect(c)
		if err := loop.addSocketAndEnableRead(fd, c); err != nil {
			log.Error("[addSocketAndEnableRead]", err)
		}
	})
}

// Start 启动 Server
func (s *Server) Start() {
	sw := sync.WaitGroupWrapper{}
	s.timingWheel.Start()

	length := len(s.workLoops)
	for i := 0; i < length; i++ {
		sw.AddAndRun(s.workLoops[i].runLoop)
	}

	sw.AddAndRun(s.loop.runLoop)
	s.running.Set(true)
	sw.Wait()
}

// Stop 关闭 Server
func (s *Server) Stop() {
	if s.running.Get() {
		s.running.Set(false)

		s.timingWheel.Stop()
		if err := s.loop.stop(); err != nil {
			log.Error(err)
		}

		for k := range s.workLoops {
			if err := s.workLoops[k].stop(); err != nil {
				log.Error(err)
			}
		}
	}

}

// Options 返回 options
func (s *Server) Options() Options {
	return *s.opts
}
