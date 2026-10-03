package core

import (
	"context"
	"sync/atomic"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/transport"
)

// switchHandler — выход-переключатель: передаёт соединение выходу, выбранному движком (target).
// Смена target действует на новые соединения, текущие не рвутся — переключение бесшовное.
type switchHandler struct {
	tag    string
	target atomic.Value // string
	om     outbound.Manager
}

var _ outbound.Handler = (*switchHandler)(nil)

func newSwitch(tag, target string, om outbound.Manager) *switchHandler {
	s := &switchHandler{tag: tag, om: om}
	s.target.Store(target)
	return s
}

func (s *switchHandler) Set(target string)  { s.target.Store(target) }
func (s *switchHandler) Get() string        { return s.target.Load().(string) }
func (s *switchHandler) Tag() string        { return s.tag }
func (s *switchHandler) Start() error       { return nil }
func (s *switchHandler) Close() error       { return nil }
func (s *switchHandler) SenderSettings() *serial.TypedMessage { return nil }
func (s *switchHandler) ProxySettings() *serial.TypedMessage  { return nil }

func (s *switchHandler) Dispatch(ctx context.Context, link *transport.Link) {
	t := s.Get()
	for i := 0; i < 4; i++ { // переключатель может указывать на другой переключатель (сервис -> sw-tcp)
		h := s.om.GetHandler(t)
		if h == nil {
			break
		}
		if sw, ok := h.(*switchHandler); ok && sw != s {
			t = sw.Get()
			continue
		}
		h.Dispatch(ctx, link)
		return
	}
	common.Interrupt(link.Writer)
	common.Interrupt(link.Reader)
}
