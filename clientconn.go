package grpc

// WithServiceConfig 返回一个可以读取service configuration的通道
func WithServiceConfig(c <-chan ServiceConfig) DialOption {
	return func(o *dialOptions) {
		o.scChan = c
	}
}

// Dial 创建了一个到给定target(这里的target不一定是ip地址或域名,可以是被naming解析的任意字符串)的连接
func Dial(target string, opts ...DialOption) (*ClientConn, error) {
	return DialContext(context.Background(), target, opts...)
}

// DialContext 创建一个到给定target的连接.ctx 用于cancel或者让连接过期.
func DialContext(ctx context.Context, target, opts ...DialOption) (conn *ClientConn, err error) {
	cc := &ClientConn{
		target: target,
		conns:  make(map[Address]*addrConn),
	}
	cc.ctx, cc.cancel = context.WithCancel(context.Background())

	// 遍历设置clientConn的参数
	for _, opt := range opts {
		opt(&cc.dopts)
	}
	cc.mkp = cc.dopts.copts.KeepaliveParams

	// 如果没有指定的dialer,通过proxyDialer建立连接
	// 底层通过dialContext建立tcp连接
	if cc.dopts.copts.Dialer == nil {
		cc.dopts.copts.Dialer = newProxyDialer(
			func(ctx context.Context, addr string) (net.conn, error) {
				return dialContext(ctx, "tcp", addr)
			},
		)
	}

	// 设置http header 的 UA
	if cc.dopts.copts.UserAgent != "" {
		cc.dopts.copts.UserAgent += " " + grpcUA
	} else {
		cc.dopts.copts.UserAgent = grpcUA
	}

	// 如果设置了timeout参数,则通过带有timeout的context去结束后续的行为
	if cc.dopts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cc.dopts.timeout)
		// cancel用于取消子ctx的动作
		defer cancel()
	}

	defer func() {
		select {
		// ctx结束
		case <-ctx.Done():
			conn, err := nil, ctx.Err()
		default:
		}

		if err != nil {
			cc.Close()
		}
	}()

	// server config 的处理
	// 可以通过withServiceConfig去指定service的配置
	scSet := false
	if cc.dopts.scChan != nil {
		// 尝试去获取一个初始的service配置,如果获取不到,则继续,后面可以继续获取
		select {
		case sc, ok := <-cc.dopts.scChan:
			if ok {
				cc.sc = sc
				scSet = true
			}
		default:
		}
	}

	// 以下是设置一些默认值
	// 指定codec:默认使用protobuf的codec
	if cc.dopts.codec == nil {
		cc.dopts.codec = protoCodec{}
	}
	// 指定codec:默认backoff的策略
	if cc.dopts.bs == nil {
		cc.dopts.bs = DefaultBackOffConfig
	}
	// 根据creds设置authority
	creds := cc.dopts.copts.TransportCredentials
	if creds != nil && creds.Info().ServerName != "" {
		cc.authority = creds.Info().ServerName
	} else if cc.dopts.insecure && cc.dopts.copts.Authority != "" {
		cc.authority = cc.dopts.copts.Authority
	} else {
		cc.authority = target
	}
	waitC := make(chan error, 1)
	//
	go func() {
		defer close(waitC)
		// 在dialoptions中未指定balancer,但是指定了serviceConfig
		// 使用serviceConfig中的LB
		if cc.dopts.balancer == nil && cc.sc.LB != nil {
			cc.dopts.balancer = cc.sc.LB
		}

		// 在options中设定了balancer, options中的balancer优先级高于serviceConfig
		if cc.dopts.balancer != nil {
			// clone一份?
			var credsClone credentials.TransportCredentials
			if creds != nil {
				credsClone = creds.Clone()
			}
			// Balancer配置,指定creds和dialer
			config := BalancerConfig{
				DialCreds: credsClone,
				Dialer:    cc.dopts.copts.Dialer,
			}
			// 启动balancer
			if err := cc.dopts.balancer.Start(target, config); err != nil {
				waitC <- err
				return
			}
			// 等待
			ch := cc.dopts.balancer.Notify()
			if ch != nil {
				// 如果指定block,则dialcontext会等待lbWatcher返回一个可用连接
				if cc.dopts.block {
					doneChan := make(chan struct{})
					go cc.lbWatcher(doneChan)
					<-doneChan
				} else {
					go cc.lbWatcher(nil)
				}
				return
			}
		}
		// 如果没有balancer,则直连target
		if err := cc.resetAddrConn(Address{Addr: target}, cc.dopts.block, nil); err != nil {
			waitC <- err
			return
		}
	}()
	// 等待返回一个可用连接,直到context结束
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-waitC:
		if err != nil {
			return nil, err
		}
	}
	// 如果设置了scChan,则需要阻塞等待一次serviceConfig
	if cc.dopts.scChan != nil && !scSet {
		select {
		case sc, ok := <-scChan:
			if ok {
				cc.sc = sc
			}
		case ctx.Done():
			return nil, ctx.Err()
		}
	}
	if cc.dopts.scChan != nil {
		go cc.scWatcher()
	}

	return cc, nil
}

// lbWatcher 观察clientConn中balancer的Notify的channel并管理响应的连接
// 如果doneChan非nil,当第一次成功的连接建立之后就关闭此channel
func (cc *ClientConn) lbWatcher(doneChan chan struct{}) {
	for addrs := range cc.dopts.balancer.Notify() {
		var (
			add []Address   // balancer通知的新增连接地址
			del []*addrConn // balancer通知列表中没有,但是在之前的conns中已经建立的连接地址,是需要被tear down的连接
		)
		cc.mu.Lock()
		// 判断哪些地址是新增的地址,添加到add中
		for _, a := range addrs {
			if _, ok := cc.conns[a]; !ok {
				add = append(add, a)
			}
		}
		// 判断哪些地址是需要被删除的地址,添加到del中
		for k, c := range cc.conns {
			var keep bool
			for _, a := range addrs {
				if k == a {
					keep = true
					break
				}
			}
			if !keep {
				del = append(del, c)
				delete(cc.conns, c.addr)
			}
		}
		cc.mu.Unlock()
		// 给新增的地址建立连接
		for _, a := range add {
			// 如果doneChan 表示dialcontext阻塞等待此调用完成
			// 则建立成功一个addr之后,就通知阻塞方
			if doneChan != nil {
				err := cc.resetAddrConn(a, true, nil)
				if err == nil {
					close(doneChan)
					doneChan = nil
				}
			} else {
				cc.resetAddrConn(a, false, nil)
			}
		}
		//  tear down 需要被删除的连接
		for _, c := range del {
			c.tearDown(errConnDrain)
		}
	}
}

// scWatcher 一直监听serviceConfig的变化
func (cc *ClientConn) scWatcher() {
	for {
		select {
		case sc, ok := <-cc.dopts.scChan:
			if !ok {
				return
			}
			cc.mu.Lock()
			cc.sc = sc
			cc.mu.Unlock()
		case <-cc.ctx.Done():
			return
		}
	}
}

// resetAddrConn 创建到addr的addrConn,并将此addrConn添加到cc.conns
// 如果addr已经有一个旧的addrConn,则把他teardown,错误设置为tearDownErr
// 如果tearDownErr为nil,则使用errConnDrain代替
func (cc *ClientConn) resetAddrConn(addr Address, block bool, tearDownErr error) error {
	ac := &addrConn{
		cc:    cc,
		addr:  addr,
		dopts: cc.dopts,
	}
	cc.mu.RLock()
	ac.dopts.copts.KeepaliveParams = cc.mkp
	cc.mu.RUnlock()
	ac.ctx, ac.cancel = context.WithCancel(cc.ctx)
	ac.stateCV = sync.NewCond(&ac.mu)
	if EnableTracing {
		ac.events = trace.NewEventLog("grpc.ClientConn", ac.addr.Addr)
	}
	// 是否指定使用https
	if !ac.dopts.insecure {
		if ac.dopts.copts.TransportCredentials == nil {
			return errNoTransportSecurity
		}
	} else {
		if ac.dopts.copts.TransportCredentials != nil {
			return errCredentialsConflict
		}
		for _, cd := range ac.dopts.copts.PerRPCCredentials {
			if cd.RequireTransportSecurity() {
				return errTransportCredentialsMissing
			}
		}
	}
	cc.mu.Lock()
	if cc.conns == nil {
		// 如果conns没有初始化,返回错误
		cc.mu.Unlock()
		return ErrClientConnClosing
	}
	stale := cc.conns[ac.addr]
	cc.conns[ac.addr] = ac
	cc.mu.Unlock()
	// 如果有旧的连接,则需要将旧的连接干掉
	if stale != nil {
		// 如果已经有一个在ac.addr上的addrconn,可能原因是
		// 1) balancer 通知了重复的地址
		// 2) 收到了goaway,一个新的ac取代旧的ac
		//    旧的ac需要从cc.conns中删除,但是底层的transport应该drain而不是close
		if tearDownErr == nil {
			// tearDownErr在resetAddrConn被以下方法调用后为nil
			// 1) dial
			// 2) lbWatcher
			// 在这些情况下,旧的ac都需要drain,而不是close
			stale.tearDown(errConnDrain)
		} else {
			stale.tearDown(tearDownErr)
		}
	}
	// 如果设置阻塞模式,则同步建立底层连接
	if block {
		if err := ac.resetTansport(false); err != nil {
			if err != errConnClosing {
				// 把错误的addrConn tear down，并从cc.conns中删除
				cc.mu.Lock()
				delete(cc.conns, ac.addr)
				cc.mu.Unlock()
				ac.tearDown(err)
			}
			if e, ok := err.(transport.ConnectionError); ok && !e.Temporary() {
				return e.Origin()
			}
			return err
		}
		// 开启一个后台进程去监控连接的状态
		go ac.transportMonitor()
	} else {
		// 开启一个routine去异步建立连接
		go func() {
			if err := ac.resetTransport(false); err != nil {
				grpclog.Warningf("Failed to dial %s: %v; please retry.", ac.addr.Addr, err)
				if err != errConnClosing {
					// 暂时保留在cc.conns中？使用err作为teardown的错误
					ac.tearDown(err)
				}
				return
			}
			ac.transportMonitor()
		}()
	}
	return nil
}

// 重置底层的transport连接(如果底层没有连接,则建立连接,如果有连接,则根据不同的情况去处理旧连接
// 同时处理addrconn的相关状态)
func (ac *addrConn) resetTransport(closeTransport bool) error {
	for retries := 0; ; retries++ {
		ac.mu.Lock()
		ac.printf("connecting")
		if ac.state == Shutdown {
			// addrConn处于Shutdown状态,说明已经调用过teardown了
			ac.mu.Unlock()
			return errConnClosing
		}
		if ac.down != nil {
			ac.down(downErrorf(false, true, "%v", errNetworkIO))
			ac.down = nil
		}
		// 修改addrconn的状态并通知相关的应用方
		ac.state = Connecting
		ac.stateCV.Broadcast()
		t := ac.transport
		ac.mu.Unlock()
		// 如果参数要求关闭原有的连接,则需要线关闭原有的连接
		if closeTransport && t != nil {
			t.Close()
		}
		// 根据backoff策略及retry次数算出需要等待的时间
		// 并取此时间于预设的最小连接时间的最大值
		sleepTime := ac.dopts.bs.backoff(retries)
		timeout := minConnectTimeout
		if timeout < sleepTime {
			timeout = sleepTime
		}
		ctx, cancel := context.WithTimeout(ac.ctx, timeout)
		connectTime := time.Now()
		sinfo := transport.TargetInfo{
			Addr:     ac.addr.Addr,
			Metadata: ac.addr.Metadata,
		}
		// 建立底层新的http2clinet transport
		newTransport, err := transport.NewClientTransport(ctx, sinfo, ac.dopts.copts)
		// 不要在err不等于nil的情况下调用cancel,会有race问题
		// 具体见 https://github.com/golang/go/issues/15078
		// 大概的意思是在go1.6的版本中,在正常情况下调用cancel会有竞争问题 ?
		if err != nil {
			cancel()
			// 错误类型转换,转换称transport的错误类型
			if e, ok := err.(transport.ConnectionError); ok && !e.Temporary() {
				return nil
			}
			grpclog.Printf("grpc: addrConn.resetTransport failed to create client transport: %v; Reconnecting to %v", err, ac.addr)
			ac.mu.Lock()
			if ac.state == Shutdown {
				ac.mu.Unlock()
				return errConnClosing
			}
			ac.errorf("transient failure: %v", err)
			ac.state = TransientFailure
			ac.stateCV.Broadcast()
			if ac.ready != nil {
				close(ac.ready)
				ac.ready = nil
			}
			ac.mu.Unlock()
			closeTransport = true
			timer := time.NewTimer(sleepTime - time.Since(connectTime))
			select {
			case <-time.C:
			case <-ac.ctx.Done():
				timer.Stop()
				return ac.ctx.Err()
			}
			timer.Stop()
			continue
		}
		ac.mu.Lock()
		ac.printf("ready")
		if ac.state == Shutdown {
			ac.mu.Unlock()
			newTransport.Close()
			return errConnClosing
		}
		// ready 表示底层的连接已经建立成功,可以通知使用方使用了
		ac.state = ready
		ac.stateCV.Broadcast()
		ac.transport = newTransport
		if ac.ready != nil {
			close(ac.ready)
			ac.ready = nil
		}
		// balancer 把建立好连接的地址使用up设置addr的连接状态
		if ac.cc.dopts.balancer != nil {
			ac.down = ac.cc.dopts.balancer.Up(ac.addr)
		}
		ac.mu.Unlock()
		return nil
	}
}

// 在一个goroutine中跟踪transport的错误
// 如果transport发生错误,则重新建立一个新连接
// 当channel关闭的时候返回
func (ac *addrConn) transportMonitor() {
	for {
		ac.mu.Lock()
		t := ac.transport
		ac.mu.Unlock()
		select {
		// 当addrconn是idle时需要检测teardown(比如没有正在处理的RPC请求)
		case <-ac.ctx.Done():
			select {
			case <-t.Error():
				t.Close()
			default:
			}
			return
		case <-t.GoAway():
			// 根据GoAway的原因调整参数
			ac.adjustParams(t.GetGoAwayReason())
			// 1.如果发生了GoAway,且没有任务的network io的错误
			// ac 被关闭了, 但是底层的transport没有关闭(transport需要等所有后续的RPC请求完成或者失败之后关闭)
			// 2.如果GoAway和一些network io的错误同时发生,ac 和其底层的transport都是关闭的
			// 在以上两种情况下, 都需要创建一个新的ac
			select {
			case <-t.Error():
				ac.cc.resetAddrConn(ac.addr, false, errNetworkIO)
			default:
				ac.cc.resetAddrConn(ac.addr, false, errConnDrain)
			}
			return
			// 如果transport发生错误
		case <-t.Error():
			select {
			case <-ac.ctx.Done():
				t.Close()
				return
			case <-t.GoAway():
				ac.adjustParams(t.GetGoAwayReason())
				ac.cc.resetAddrConn(ac.addr, false, ErrNetworkIO)
				return
			default:
			}
			ac.mu.Lock()
			if ac.state == Shutdown {
				ac.mu.Unlock()
				return
			}
			// ac 的状态变化都要通知使用方
			ac.state = TransientFailure
			ac.stateCV.Broadcast()
			ac.mu.Unlock()
			if err := ac.resetTransport(true); err != nil {
				ac.mu.Lock()
				ac.printf("transport exiting:%v", err)
				ac.mu.Unlock()
				grpclog.Printf("grpc:addrconn.transport monitor exits due to: %v", err)
				if err != errConnClosing {
					ac.tearDown(err)
				}
				return
			}
		}
	}
}
