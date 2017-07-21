package grpc

var (
	// errDiasble 表示这个地址没有使用proxy
	errDisable = errors.New("proxy is disable for the address")
	// 从环境变量中获取proxy的信息(HTTP_PROXY...)
	httpProxyFromEnvironment = http.ProxyFromEnvironment
)

// mapAddress返回
func mapAddress(ctx context.Context, address string) (string, error) {
	req := &http.Request{
		URL: &url.URL{
			Scheme: "https",
			Host:   address,
		},
	}

	url, err := httpProxyFromEnvironment(req)
	if err != nil {
		return "", err
	}
	if url == "" {
		return "", errDisable
	}
	return url.Host, nil
}

// newProxyDialer 返回一个dialer，看是否需要连接proxy
// 返回的dialer先检查如果需要一个proxy,则先用dialer连上proxy,
// 做一步HTTP CONNECT的握手,然后返回连接
func newProxyDialer(dialer func(context.Context, string) (net.Conn, error)) func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, addr string) (net.Conn, error) {
		var skipHandshake bool
		newAddr, err := mapAddress(ctx, addr)
		if err != nil {
			if err != errDisable {
				return nil, err
			}
			skipHandshake = true
			newAddr = addr
		}

		conn, err := dialer(ctx, newAddr)
		if err != nil {
			return
		}
		if !skipHandshake {
			conn, err = doHTTPConnectHandshake(ctx, conn, addr)
		}
		return
	}
}
