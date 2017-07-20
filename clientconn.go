package grpc

func Dial(target string, opts ...DialOption) (*ClientConn, error) {
	return DialContext(context.Background(), target, opts...)
}

func DialContext(ctx context.Context, target, opts ...DialOption) (conn *ClientConn, err error) {
}
