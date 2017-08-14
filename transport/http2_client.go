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
		// 如果是临时性的错误,需要重连
		if opts.FailOnNonTempDialError {
			return nil, connectionErrorf(isTemporyary(err), err, "transport: error while dialing: %v", err)
		}
		return nil, connectionErrorf(true, err, "transport: error while dialing: %v", err)
	}

	// 后续任何的错误都会导致函数返回,并关闭底层的连接
	defer func(conn net.Conn) {
		if err != nil {
			conn.Close()
		}
	}(conn)
	var (
		isSecure bool
		authInfo credentials.AuthInfo
	)
	if creds := opts.TransportCredentials; creds != nil {
		scheme := "https"
		// https的握手过程
		conn, authInfo, err = creds.ClientHandshake(ctx, addr.Addr, conn)
		if err != nil {
			// 握手过程的错误一般都是非临时错误
			// 需要避免去重试,如证书错误
			temp := isTemporyary(err)
			return nil, connectionErrorf(temp, err, "transport: authentication handshake failed: %v", err)
		}
		isSecure = true
	}
	kp := opts.KeepaliveParams
	// 校验keepalive参数
	// 并在无效的情况下设置默认值
	if kp.Time == 0 {
		kp.Time = defaultClientKeepaliveTime
	}
	if kp.Timeout == 0 {
		kp.Timeout = defaultClientKeepaliveTimeout
	}
	// 初始化的连接窗口
	// 取较大的值
	icwz := int32(initialConnWindowSize)
	if opts.InitialConnWindowSize >= defaultWindowSize {
		icwz = opts.InitialConnWindowSize
	}
	var buf bytes.Buffer
}
