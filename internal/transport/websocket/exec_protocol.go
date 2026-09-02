package websockettransport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
)

const (
	TerminalSubprotocol  = "ani.terminal.v1"
	VNCSubprotocol       = "ani.vnc.v1"
	maxTerminalDimension = 4096
)

type clientFrame struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
}
type serverFrame struct {
	Type    string `json:"type"`
	Data    string `json:"data,omitempty"`
	Code    any    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func decodeClientFrame(raw []byte, maxStdin int64) (clientFrame, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var frame clientFrame
	if err := decoder.Decode(&frame); err != nil {
		return clientFrame{}, err
	}
	if err := ensureEOF(decoder); err != nil {
		return clientFrame{}, err
	}
	switch frame.Type {
	case "stdin":
		if int64(len(frame.Data)) > maxStdin {
			return clientFrame{}, errors.New("stdin frame exceeds limit")
		}
	case "resize":
		if frame.Rows == 0 || frame.Cols == 0 || frame.Rows > maxTerminalDimension || frame.Cols > maxTerminalDimension {
			return clientFrame{}, errors.New("invalid terminal size")
		}
	default:
		return clientFrame{}, errors.New("unsupported frame type")
	}
	return frame, nil
}

func encodeServerFrame(frame serverFrame) []byte { encoded, _ := json.Marshal(frame); return encoded }
func ensureEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
func terminalSize(frame clientFrame) session.TerminalSize {
	return session.TerminalSize{Rows: frame.Rows, Cols: frame.Cols}
}
