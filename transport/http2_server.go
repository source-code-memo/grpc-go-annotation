package transport

// http2Server 使用http2实现ServerTransport的结构
type htt2Server struct {
}

// HandleStreams 使用给定的handler处理接收进来的流
// 通常这个是跑在一个单独的routine中
// traceCtx 给ctx绑定trace,并返回新的context
func (t *http2Server) HandleStreams(handle func(*Stream), traceCtx func(context.Context, string) context.Context) {
	// 检查client preface的有效性
	preface := make([]byte, len(clientPreface))
	if _, err := io.ReadFull(t.conn, preface); err != nil {
		// 只有在不是正常的流结束情况下才记日志
		// 比如均衡器在做打开/关闭socket的操作
		if err != io.EOF {
			errorf("transport: http2Server.HandleStreams failed to receive the preface from client: %v", err)
		}
		t.Close()
		return
	}
	if !bytes.Equal(preface, clientPreface) {
		errorf("transport: http2Server.HandleStreams received bogus greetinf from client: %q", preface)
		t.Close()
		return
	}

	frame, err := t.framer.readFrame()
	// 正常的client 关闭连接, 不记录日志
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		t.Close()
		return
	}
	// 读取setting frame 错误
	if err != nil {
		errorf("transport: http2Server.HandleStreams failed to read initial settings frame: %v", err)
		t.Close()
		return
	}
	atomic.StoreUint32(&t.activity, 1)
	sf, ok := frame.(*http2.SettingsFrame)
	if !ok {
		errorf("transport: http2Server.HandleStreams saw invalid preface type %T from client", frame)
		t.Close()
		return
	}
	// 处理setting frame
	t.handleSettings(sf)

	for {
		frame, err := t.framer.readFrame()
		atomic.StoreUint32(&t.activity, 1)
		if err != nil {
			// 如果是http2 stream error
			if se, ok := err.(http2.StreamError); ok {
				t.mu.Lock()
				s := t.activiStreams[se.StreamID]
				t.mu.Unlock()
				// 关闭错误的stream
				if s != nil {
					t.closeStream(s)
				}
				t.controlBuf.put(&resetStream{se: StreamID, se.Code})
				continue
			}
			// 连接正常关闭
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				t.Close()
				return
			}
			// 连接异常关闭
			warningf("transport: http2Server.HandleStreams failed to read frame: %v", err)
			t.Close()
			return
		}
		// 针对不同的frame类型做不同的处理
		switch frame := frame.(type) {
		case *http2.MetaHeadersFrame:
			if t.operateHeaders(frame, handle, traceCtx) {
				t.Close()
				break
			}
		case *http2.DataFrame:
			t.handleDate(frame)
		case *http2.RSTStreamFrame:
			t.handleRSTStream(frame)
		case *http2.SettingsFrame:
			t.handleSettings(frame)
		case *http2.PingFrame:
			t.handlePing(frame)
		case *http2.WindowUpdateFrame:
			t.handleWindowUpdate(frame)
		case *http2.GoAwayFame:
			// TODO
		default:
			errorf("transport: http2Server.HandleStreams found unhandled frame type %v.", frame)
		}
	}
}

// 根据setting frmae的参数,调整transport相关的参数
func (t *http2Server) handleSettings(f *http2.SettingsFrame) {
	if f.IsAck() {
		return
	}
	var ss []http2.Setting
	f.ForeachSetting(func(s http2.Setting) error {
		ss = append(ss, s)
		return nil
	})
	//
	t.controlBuf.Put(&settings{ack: true, ss: ss})
}
