package grpc

// Server 是处理RPC请求的server
type Server struct {
	opts options

	mu     sync.Mutex // 以下的变量需要锁
	lis    map[net.Listener]bool
	conns  map[io.Closer]bool
	serve  bool
	drain  bool
	ctx    context.Context
	cancel context.CancelFunc

	// CondVar 让GracefulStop 阻塞等待所有剩余的RPC结束后
	// 让所有的旧的transport go away
	cv     *sync.Cond
	m      map[string]*service // service name -> service info 通过service name获取service info
	events trace.EventLog
}

// NewServer 创建一个grpc Server, 但没有注册任何的服务
// 也还不能接受任何的请求
func NewServer(opts ...ServerOptions) *Server {
}
