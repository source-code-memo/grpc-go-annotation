// package transport 定义和实现了面向消息的通信通道,去完成不同的数据处理(如RPC)

package transport

// ConnectOptions 涵盖了所有的和server通信的选项配置
type ConnectOptions struct {
	// UserAgent 是应用的UA
	UserAgent string
	// Authority :使用一个伪的头信息(authority)
	// 这个字段在TransportCredentials设置的情况下是无效的
	Authority string
	// Dialer 是如何连接一个网络地址的方法
	Dialer func(context.Context, string) (net.Conn, err)
	// FailOnNonTempDialError 规定了如果有一个非临时的dial错误,grpc是否失败
	FailOnNonTempDialError bool
	// PerRPCCredentials 存储了处理RPC请求所需的PerRPCCredentials
	PerRPCCredentials []credentials.PerRPCCredentials
	// TransportCredentials 存储了建立一个客户端连接所需的鉴权信息
	TransportCredentials credentials.TransportCredentials
	// keepaliveParams 存储了keepalive 的参数
	KeepaliveParams keepalive.ClientParameters
	// StatsHandler 存储了stat的状态处理
	StatsHandler stats.Handler
	// InitialWindowSize 设置stream流的初始窗口
	InitialWindowSize int32
	// InitialConnWindowSize 设置连接的初始窗口
	InitialConnWindowSize int32
}

// TargetInfo 包含了目标的信息如网络地址和元信息
type TargetInfo struct {
	Addr     string
	Metadata interface{}
}

// NewClientTransport 使用必备的参数ConnectOptions建立一个transport
// 把这个transport返回给dialer
func NewClientTransport(ctx context.Context, target TargetInfo, opts ConnectOptions) (ClientTransport, error) {
	return newHTTP2Client(ctx, target, opts)
}
