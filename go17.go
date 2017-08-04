package grpc

// dialContext 用指定的network方式(tcp...)等和address建立连接
func dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

//
func sendHTTPRequest(ctx context.Context, req *http.Request, conn net.Conn) error {
	req := req.WithContext(ctx)
	if err := req.Write(conn); err != nil {
		return err
	}
	return nil
}
