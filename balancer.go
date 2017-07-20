package grpc

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
