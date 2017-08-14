package transport

func dial(ctx context.Context, fn func(context.Context, string) (net.Conn, error), addr string) (net.Conn, error) {
	if fn != nil {
		return fn(ctx, addr)
	}
	return dialContext(ctx, "tcp", addr)
}

// newHTTP2Client 建立一个到addr的clientTransport的连接
// 并开始在此连接上接收消息
func newHTTP2Client(ctx context.Context, addr TargetInfo, opts ConnectOptions) (clientTransport, error) {
	scheme := "http"
	conn, err := dial(ctx, opts.Dialer, addr.Addr)
	if err != nil {
		// 如果设置了只能在非临时的Dial错误的时候才能失败
		if opts.FailOnNonTempDialError {
			return nil, connectionErrorf(isTemporyary(err), err, "transport: error while dialing: %v", err)
		}
		return nil, connectionErrorf(true)
	}
}
