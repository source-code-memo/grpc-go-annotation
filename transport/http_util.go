package transport

type framer struct {
	newWriters int32
	reader     io.Reader
	writer     *bufio.Writer
	fr         *http2.Framer
}

func newFramer(conn net.Conn) *framer {
	f := &famer{
		reader: bufio.NewReaderSize(conn, http2IOBufSize),
		writer: bufio.NewWriterSize(conn, http2IOBufSize),
	}
	f.fr = http2.NewFramer(f.writer, f.reader)
	// 重复使用framer, 以减少gc
	// 这样做使Frames 不是并发安全的,当你读写这个framer,随后的调用ReadFrame会覆盖这个frame
	f.fr.SetReuseFrames()
	f.fr.ReadMetaHeaders = hpack.NewDecoder(http2InitHeaderTableSize, nil)
	return f
}

func (f *framer) readFrame(http2.Frame, error) {
	return f.fr.ReadFrame()
}
