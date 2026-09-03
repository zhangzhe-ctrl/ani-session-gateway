package connectedsession

import "time"

type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

type Timer interface {
	C() <-chan time.Time
	Reset(time.Duration) bool
	Stop() bool
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

func (SystemClock) NewTimer(after time.Duration) Timer {
	return systemTimer{timer: time.NewTimer(after)}
}

type systemTimer struct{ timer *time.Timer }

func (t systemTimer) C() <-chan time.Time        { return t.timer.C }
func (t systemTimer) Reset(d time.Duration) bool { return t.timer.Reset(d) }
func (t systemTimer) Stop() bool                 { return t.timer.Stop() }
