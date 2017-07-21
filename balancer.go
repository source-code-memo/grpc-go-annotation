package grpc

// Address 表示一个client所连接上的server地址信息
type Address struct {
	// server 的地址
	Addr string
	// metadata 表示server的相关信息，可以用来决定流量负载均衡的策略
	Metadata interface{}
}

// BalancerConfig 表示 Balnacer的配置
type BalancerConfig struct {
	// DialCreds 是传输层的证书，Balancer的实现用此证书去和远端的负载均衡server建立连接。如果不需要加密传输，则可以忽略此证书
	DialCreds credentials.TransportCredentials
	// Dialer 是Balancer用于和远端的负载均衡server建立连接的dialer,如果不需要和远端负载均衡server建立连接，则可以忽略
	Dialer func(context.Context, string) (net.Conn, error)
}

// Balancer 为RPC选择地址,负载均衡器
type Balancer interface {
	// Start 函数做一些启动Balancer的初始化的工作。比如：启动name resolution 和 watch update. 这些会在dial的过程中被调用
	Start(target string, config BalancerConfig) error
	// Up 表示grpc有一个连上addr server的链接。如果连接处于丢失或者关闭的状态，则返回一个down的错误处理函数
	// 目前并不十分清楚如何构建和利用这有意义的错误参数down。可能在后面会给出比较实际的指导用法
	Up(addr Address) (down func(error))
	/*
			Get 获取一个对应ctx的RPC的负载均衡server的address。
			1 如果返回的是一个已经连接的地址，grpc内部会把rpc的请求转到这个地址的connection
			2 如果返回的是一个还没有连接上但正在构建的连接上（通过 notify 初始化），如果rpc请求是快速失败模式，grpc内部会将此rpc请求失败，连接处于暂时失败或者关闭状态；或者将此rpc的请求转发到其他的连接上
			3 如果返回的地址上没有连接，grpc内部会认为是一个错误，会将对应的rpc请求做失败处理

		   因此，当写一个传统的Balancer时，有以下一些建议：
		   如果 opts.BlockingWait 是true， 应该返回一个已经连接上的address，如果没有已经连接上的address，则阻塞。当阻塞的时候，需要考虑超时或者ctx的取消操作。如果 opts.BlockingWait 时false( 用于fail-fast的rpc）， 应该直接返回一个通过Notify通知过的address而不是阻塞住。
		   函数的返回值put在rpc完成或者失败的时候调用一次。put 可以收集和汇报rpc的状态给远端的负载均衡器。
		   这个函数应该只返回那些负载均衡器不能够自我恢复的错误。
		   如果有错误返回，则grpc会把rpc请求置为失败。
	*/
	Get(ctx context.COntext, opts BalancerGetOptions) (addr Address, put func(), err error)
	/*
	   Notify 返回一个grpc内部使用的channel，用于watch grpc需要连接的address的变化。address可以从一个name resolver 中或者一个远程的负载均衡器中获取。grpc内部会将它和已经存在的已连接的 address比较。如果 address 均衡器通知了一个之前不存在的已连接上的address（新服务上线），grpc开始连接这个新的地址。如果一个地址在已经连接的地址列表中但不在通知列表中（服务下线），则grpc优雅的关闭已连接上的连接。其他情况，什么都不做。注意，Address 这个slice 应该是需要连接的完整的地址列表。不是部分差值列表。
	*/
	Notify() <-chan []Address
}

type roundRobin struct {
	r      naming.Resolver // 名字解析器
	w      naming.Watcher  // 名字watcher
	addrs  []*addrInfo     // clinet应该连接的所有的潜在地址
	mu     sync.Mutex
	addrch chan []Address // 用于grpc内部通知客户端可以连接的address的列表
	next   int            // 返回Get的一下个address
	waitCh chan struct{}  // 如果没有一个可用的以连接的地址，则此channel用于阻塞
	done   bool           // balancer 是否已经被关闭了
}

// addrInfo 表示一个地址信息以及它的连接状态
type addrInfo struct {
	addr      Address
	connected bool
}

// RoundRobin 返回一个以round robin的方式选择地址的balancer.使用r去watch名字解析的更新
// 并相应的更新地址
func RoundRobin(r naming.Resolver) Balancer {
	return &roundRobin{r: r}
}

// watch 地址的变化，这个也是naming 的 watcher 需要提供的功能
func (rr *roundRobin) watchAddrUpdates() error {
	// Next 返回一个或多个updates,具体见naming
	updates, err := rr.w.Next()
	if err != nil {
		grpclog.Warningf("grpc: the naming wather stops working due to %v.", err)
		return err
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()

	// updates 是Update的slice，Update 表示一个name resolution的更新
	for _, update := range updates {
		addr := Address{
			Addr:     update.Addr,
			Metadate: update.Metadata,
		}
		// 根据update中不同的操作类型,去做不同的操作
		switch update.Op {
		// 如果是增加操作，则将新的地址添加到addrs 中，但需要检查新的地址是否已经存在于已有的连接地址中
		case naming.Add:
			var exist bool
			for _, v := range rr.addrs {
				if addr == v.addr {
					exist = true
					grpclog.Infoln("grpc: the name resolver wanted to add an existing address: ", addr)
					break
				}
			}
			if exist {
				continue
			}
			rr.addrs = append(r.addrs, &addrInfo{addr: addr})
			// 如果是删除操作，则将此addr从addrs的列表中删除
		case naming.Delete:
			for i, v := range rr.addrs {
				if addr == v.addr {
					copy(rr.addrs[i:], rr.addrs[i+1:])
					rr.addrs = rr.addrs[:len(rr.addrs)-1]
					break
				}
			}
		default:
			grpclog.Errorln("Unknown update OP", update.Op)
		}
	}
}

func (rr *roundRobin) Start(targe string, config BalancerConfig) error {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	// balancer 已经结束
	if rr.done {
		return ErrClientConnClosing
	}
	// 如果没有使用任何的name resolver，就不需要做 name resolution。在这种情况下，target被加进addrs列表中唯一可用的address, addrCh也是为空的。
	if rr.r == nil {
		rr.addrs = append(rr.addrs, &addrInfo{addr: Address{Addr: target}})
		return nil
	}

	// 如果有name resolver, 则使用name resolver去解析target,通过另外一个routine去watch更新，然后通过addrCh去更新rr.addrs地址列表
	w, err := rr.r.Resolve(target)
	rr.addrCh = make(chan []Address, 1)
	go func() {
		for {
			if err := rr.watchAddrUpdates(); err != nil {
				return
			}
		}
	}()
	return nil
}

// Up是用来设置addr的连接状态的,如果此时有附加的Get操作，则发送通知
func (rr *roundRobin) Up(addr Address) func(error) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	var cnt int
	// 遍历addrs的列表，将对应的addr的连接状态置为已连接
	// 统计已连接的连接数cnt
	for _, a := range rr.addrs {
		if a.addr == addr {
			if a.connected {
				return nil
			}
			a.connected = true
		}
		if a.connected {
			cnt++
		}
	}
	// 如果addr是唯一一个已经连接上的address。通知那个正在被Get阻塞的调用者
	if cnt == 1 && rr.waitCh != nil {
		close(rr.waitCh)
		rr.waitCh = nil
	}
	// 如果此连接在Up调用之前是处于非连接状态，调用Up之后，返回一个将此连接重置为非连接状态的处理函数
	return func(err error) {
		rr.down(addr, err)
	}
}

// down 重置addr的连接状态为未连接
func (rr *roundRobin) down(addr Address, err error) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	for _, a := range rr.addrs {
		if a.addr == addr {
			a.connected = false
			break
		}
	}
}

// Get获取循环的下一个地址
func (rr *roundRobin) Get(ctx context.Context, opts BalancerGetOptions) (addr Address, put func(), err error) {
	var ch chan struct{}
	rr.mu.Lock()
	// 如果rr的balancer已经结束,返回错误
	if rr.done {
		rr.mu.Unlock()
		err = ErrClientConnClosing
		return
	}

	// 如果rr中有addrs的列表,则根据上次的next在轮询选择下一个地址,并更新next
	if len(rr.addrs) > 0 {
		if rr.next >= len(rr.addrs) {
			rr.next = 0
		}
		next := r.next
		for {
			a := rr.addrs[next]
			next = (next + 1) % len(rr.addrs)
			// 如果next是已连接上的connection,则返回此连接，并更新next索引
			if a.connected {
				addr = a.addr
				rr.next = next
				rr.mu.Unlock()
				return
			}
			if next == rr.next {
				// 如果遍历一边addr的列表,发现没有可用的连接,则接下来会根据是否阻塞的设置进行不同的处理
				break
			}
		}
	}

	// 如果是没有设置阻塞等待, 则应该快速返回错误或者任意一个
	if !opts.BlockingWait {
		if len(rr.addrs) == 0 {
			rr.mu.Unlock()
			err = Errorf(codes.Unavailable, "there is no address available")
			return
		}

		// 返回addr列表的下一个，对于需要快速失败的rpc请求
		addr = rr.addrs[rr.next].addr
		rr.next++
		rr.mu.Unlock()
		return
	}
	// 对于不需要快速失败的rpc请求，等待waitCh的通知
	if rr.waitCh != nil {
		ch = make(chan struct{})
		rr.waitCh = ch
	} else {
		ch = rr.waitCh
	}

	rr.mu.Unlock()
	// 等待ch的通知或者context的结束
	for {
		select {
		// context 结束
		case <-ctx.Done():
			err = ctx.Err()
			return
		// 收到ch的通知，表示addrs中可能已经有可用的连接了
		// 这块的处理和最开始从addr列表中拿addr是相同的操作
		case <-ch:
			rr.mu.Lock()
			if rr.done {
				rr.mu.Unlock()
				err = ErrClientConnClosing
				return
			}

			if len(rr.addrs) > 0 {
				if rr.next >= len(rr.addrs) { // 上次已经轮询到列表的结尾
					rr.next = 0
				}
				next := r.next
				for { // rr的轮询算法
					a := rr.addrs[next]
					next = (next + 1) % len(rr.addrs)
					if a.connected {
						addr = a.addr
						rr.next = next
						rr.mu.Unlock()
						return
					}
					if next == rr.next { // 轮询了一圈没有找到已经连接上的addr
						break
					}
				}
			}
			// 可能新加的连接又被其他的函数又删除了(目前的到没有从addr列表中删除addr的函数，down函数是把列表中某个addr的连接状态置为false)
			if rr.waitCh != nil {
				ch = make(chan struct{})
				rr.waitCh = ch
			} else {
				ch = rr.waitCh
			}
			rr.mu.Unlock()
		}
	}
}

// Notify 函数返回 address slice的channel
// 配置了balancer的client可以根据channel返回值建立连接
func (rr *roundRobin) Notify() <-chan []Address {
	return rr.addrCh
}

// Close关闭和释放rr的资源，主要是channel的资源
func (rr *roundRobin) Close() error {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.done {
		return errBalancerClosed
	}
	rr.done = true
	if rr.w != nil {
		rr.w.Close()
	}
	if rr.waitCh != nil {
		close(rr.waitCh)
		rr.waitCh = nil
	}
	if rr.addrCh != nil {
		close(rr.addrCh)
		rr.addrCh = nil
	}
	return nil
}
