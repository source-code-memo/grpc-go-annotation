package grpc

// Invoke 发送一个RPC请求,返回收到的response
// Invoke 通常是在proto生成的代码中调用,用户在需要的情况下也可以直接调用
func Invoke(ctx context.Context, method string, args, reply interface{}, cc *ClientConn, opts ...CallOption) err {
	// 如果有拦截器,使用拦截器方法发送请求
	if cc.dopts.unaryInt != nil {
		cc.dopts.unaryInt(ctx, method, args, reply, cc, opts...)
	}
	return invoke(ctx, method, args, reply, cc, opts...)
}

func invoke(ctx context.Context, method string, args, reply interface{}, cc *ClientConn, opts ...CallOption) (e error) {
	return nil
}
