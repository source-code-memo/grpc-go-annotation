package grpc

// ServiceDesc 代表一个RPC service的详细描述
type ServiceDesc struct {
	ServiceName string

	// 指向service 接口
	// 用来检查是否用户提供的实现满足接口的需求
	HandlerType interface{}
	Methods     []MethodDesc
	Streams     []StreamDesc
	Metadata    interface{}
}

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
func NewServer(opt ...ServerOptions) *Server {
	opts := defaultServerOptions
	// 设置opts
	for _, o := range opt {
		o(&opts)
	}
	if opts.codec == nil {
		// 设置默认的codec
		opts.codec = protoCodec{}
	}
	s := &server{
		lis:   make(map[net.Listener]bool),
		opts:  opts,
		conns: make(map[io.Closer]bool),
		m:     make(map[string]*service),
	}
	s.cv = sync.NewCond(&s.mu)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	if EnableTracing {
		_, file, line, _ := runtime.Caller(1)
		s.events = trace.NewEventLog("grpc.Server", fmt.Sprintf("%s:%d", file, line))
	}
	return s
}

// RegisterService 注册一个service和其实现给gRPC server
// 这里是在IDL生成的代码中调用的, 这必须要在服务被调用前注册
func (s *Server) RegisterService(sd *ServiceDesc, ss interface{}) {
	ht := reflect.TypeOf(sd.HandlerType).Elem()
	st := reflect.TypeOf(ss)
	if !st.Implements(ht) {
		grpclog.Fatalf("grpc: Server.RegisterService found the handler of type %v that does not satisfy %v", st, ht)
	}
	s.register(sd, ss)
}

func (s *Server) register(sd *ServiceDesc, ss interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.printf("RegisterServer(%q)", sd.ServiceName)
	// 需要在server启动之前注册相关的service
	if s.serve {
		grpclog.Fatalf("grpc: Server.RegisterService after Server.Serve for %q", sd.ServiceName)
	}
	if _, ok := s.m[sd.ServiceName]; ok {
		grpclog.Fatalf("grpc: Server.RegisterService found duplicate service registration for %q", sd.ServiceName)
	}
	srv := &service{
		server: ss,
		md:     make(map[string]*MethodDesc),
		sd:     make(map[string]*StreamDesc),
		mdata:  sd.Metadata,
	}
	for i := range sd.Methods {
		d := &sd.methods[i]
		srv.md[d.MethodName] = d
	}
	for i := range sd.Strams {
		d := &sd.Streams[i]
		srv.sd[d.StreamName] = d
	}
	s.m[sd.ServiceName] = srv
}

// Serve 接收lis上到来的连接,创建一个新的ServerTransport
// 并且每一个连接一个routine
// 每个service的routine读取grpc的请求,然后调用已经注册的handlers返回调用结果
// Serve 在lis.Accept失败的时候返回fatal错误
// lis在返回时关闭
// Serve 总是返回non-nil的错误
func (s *Server) Serve(lis net.Listenr) error {
	s.mu.Lock()
	s.printf("serving")
	s.serve = true
	if s.lis == nil {
		s.mu.Unlock()
		lis.Close()
		return ErrServerStopped
	}
	s.lis[lis] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if lis != nil && s.lis[lis] {
			lis.Close()
			delete(s.lis[lis])
		}
		s.mu.Unlock()
	}()

	var tempDelay time.Duration // 如果accept失败了,sleep多长时间

	for {
		rawconn, err := lis.Accept()
		// accept 失败
		if err != nil {
			// 如果时临时错误,则有机会重试
			if ne, ok := err.(interface {
				Temporary() bool
			}); ok && ne.Temporary() {
				// 设置临时的延迟等待时间,以两倍的速度增长,最长时间时1s
				if tempDelay == 0 {
					tempDelay = 5 * time.MilliSecond
				} else {
					tempDelay *= 2
				}
				if max := 1 * time.Second; tempDelay > max {
					tempDelay = max
				}
				s.mu.Lock()
				s.printf("Accept error:%v; retrying in %v", err, tempDelay)
				s.mu.Unlock()
				// 等待tempDelay的时长(等待临时性的错误恢复)
				timer := time.NewTimer(tempDelay)
				select {
				case <-timer.C:
				case <-s.ctx.Done():
				}
				timer.Stop()
				continue
			}
			// 如果不是临时错误,则直接返回错误
			s.mu.Lock()
			s.printf("done serving; Accept = %v", err)
			s.mu.Unlock()
			return err
		}
		// 起一个routine去处理rawconn
		// 这样不会阻塞Accept的循环
		go s.handleRawConn(rawconn)
	}
}
