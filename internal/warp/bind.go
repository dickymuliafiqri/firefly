package warp

import (
	"golang.zx2c4.com/wireguard/conn"
)

// warpBind wraps a conn.Bind to inject Cloudflare WARP client_id into the 3 reserved WireGuard header bytes.
type warpBind struct {
	conn.Bind
	reserved [3]byte
}

func newWarpBind(b conn.Bind, reserved [3]byte) conn.Bind {
	return &warpBind{Bind: b, reserved: reserved}
}

func (w *warpBind) Send(buffs [][]byte, ep conn.Endpoint) error {
	for _, b := range buffs {
		if len(b) >= 4 {
			b[1] = w.reserved[0]
			b[2] = w.reserved[1]
			b[3] = w.reserved[2]
		}
	}
	return w.Bind.Send(buffs, ep)
}

func (w *warpBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	fns, actualPort, err := w.Bind.Open(port)
	if err != nil {
		return nil, 0, err
	}
	wrappedFns := make([]conn.ReceiveFunc, len(fns))
	for i, fn := range fns {
		origFn := fn
		wrappedFns[i] = func(buffs [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
			n, err := origFn(buffs, sizes, eps)
			for j := 0; j < n; j++ {
				if sizes[j] >= 4 {
					buffs[j][1] = 0
					buffs[j][2] = 0
					buffs[j][3] = 0
				}
			}
			return n, err
		}
	}
	return wrappedFns, actualPort, nil
}
