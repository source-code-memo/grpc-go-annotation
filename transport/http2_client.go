package transport

/*
type Addr interface {
	NetWork() string // name of the network (for example, "tcp", "udp")
	String() string // string form of address (for example, "192.0.2.1:25", "[2001:db8::1]:80")
}
*/
// http2Client 使用HTTP2实现了clientTransport的接口
type http2Client struct {
	ctx         context.Context
	target      string // server 名字或者地址
	userAgent   string
	md          interface{} // metadata
	conn        net.Conn    // 底层的通信通道
	retmoteAddr net.Addr
	locateAddr  net.Addr
	authInfo    credentials.AuthInfo // 连接的认证信息
	nextID      uint32               // 下一个需要使用的streamID

	// writeableChan 同步写的权限去transport
	// writer 需要通过往writeableChan发送数据来获取writelock
	// 通过从writeableChan接收数据来释放锁
	writeableChan chan int
	// shutdownChan 在Close被调用的时候关闭
	//
	shutdownChan chan struct{}
	// errorChan是用来通知调用放I/O错误
	errorChan chan struct{}
	// goAway 在关闭时用来通知上层的调用方(如 addrConn.transportMonitor)
	// 告诉调用方server在这个transport上发送了GoAway信号
	goAway chan struct{}
	// awakenKeepalive 用于在底层transport休眠的时候唤醒它(发送ping的心跳)
	awakenKeepalive chan struct{}

	framer *framer
	hBuf   *bytes.Buffer  // HPACK的编码buffer
	hEnc   *hpack.Encoder // HPACK的编码器

	// controlBuf 传递所有的控制controller相关的任务
	// 如: window的更新, reset流, 变量的设置
	controlBuf *controlBuffer
	fc         *inFlow
	// sendQuotaPool 提供了带外信息的流控
	sendQuotaPool *quotaPool
	// streamsQuota 限制并发stream的最大数目
	streamsQuota *quotaPool

	// 使用的scheme, https 或者 http
	scheme string

	isSecure bool

	creds []credentials.PerRPCCredentials

	// 跟踪transport上读取行为
	// 1表示 true ,0 是false
	activity uint32
	kp       keepalive.ClientParameters

	statsHandler stats.Handler

	initialWindownSize int32

	bdpEst          *bdpEstimator
	outQuotaVersion uint32

	mu            sync.Mutex // 保护以下的变量
	state         transportState
	activeStreams map[uint32]*Stream
	// 并发的stream的最大数目
	maxStreams int
	// 对方设置的每个stream的带外流控制的窗口大小
	streamSendQuota uint32
	// prevGoAway ID 记录 前一个GOAway frame中的Last-Stream-ID
	prevGoAwayID uint32
	// goAwayReason 记录http2.ErrCode和收到的GoAway帧中的debug数据
	goAwayReason GoAwayReason
}

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

	t := &http2Client{
		ctx:        ctx,
		target:     addr.Addr,
		userAgent:  opts.UserAgent,
		md:         addr.Metadata,
		conn:       conn,
		remoteAddr: conn.RemoteAddr(),
		localAddr:  conn.LocalAddr(),
		authInfo:   authInfo,
		// client 初始的流id是基数, 从1开始
		nextID:          1,
		writeableChan:   make(chan int, 1),
		shutdownChan:    make(chan struct{}),
		errorChan:       make(chan struct{}),
		goAway:          make(chan struct{}),
		awakenKeepalive: make(chan struct{}, 1),
		framer:          newFramer(conn),
		hBuf:            &buf,
		hEnc:            hpack.NewEncode(&buf),
		controlBuf:      newControlBuffer(),
		fc:              &inFlow{limit: uint32(icwz)},
		sendQuotaPool:   newQuotaPool(defaultWindowSize),
		scheme:          scheme,
		state:           reachable,
		// 活跃的流
		activeStreams: make(map[uint32]*Stream),
		// 和scheme相对应
		isSecure: isSecure,
		creds:    opts.PerRPCCredentials,
		// 同时最大的流client
		maxStreams:        defaultMaxStreamClient,
		streamsQuota:      newQuotaPool(defaultMaxStreamClient),
		streamSendQuota:   defaultWindoSize,
		kp:                kp,
		statsHandler:      opts.StatsHandler,
		initialWindowSize: initalWindowSize, // initalWindowSize = defaultWindoSize
	}

	if opts.InitialWindowSize >= defaultWindoSize {
		t.initialWindownSize = opts.InitialWindowSize
		dynamicWindow = false
	}
	// 如果opts中设置的初始窗口大小大于默认值,则使用opts中的值,不需要动态调整
	// 否则会动态调整
	if dynamicWindow {
		t.bdpEst = &bdpEstimator{
			bdp:               initialWindowSize,
			updateFlowControl: t.updateFlowControl,
		}
	}

	// 这里往awakenKeepalive中写入之后,它就不能再写了
	// 只有keepalive这个goroutine能够使它再次能写
	// keepalive routine的作用就是保活
	t.awakenKeepalive <- struct{}{}
	if t.statsHandler != nil {
		t.ctx = t.statsHandler.TagConn(t.ctx, &stats.ConnTagInfo{
			RemoteAddr: t.remoteAddr,
			LocalAddr:  t.localAddr,
		})
		connBegin := &stats.ConnBegin{
			Client: true,
		}
		t.statsHandler.HandleConn(t.ctx, connBegin)
	}
}
