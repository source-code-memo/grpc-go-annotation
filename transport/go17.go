package transport

// golang提供了Dialer, Dialer 提供了Dial 和 DialContext 两个接口
func dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}
