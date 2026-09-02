package memory

import (
	"testing"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/storetest"
)

func TestContract(t *testing.T) { storetest.Run(t, func(*testing.T) session.Store { return New() }) }
