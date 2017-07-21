package naming

// naming包定义了naming的API和相关的数据结构
// 目前这些接口还都是实验性的,未来可能会发生变化

// Operation 定义了name 解析变化相关的操作
type Operation uint8

const (
	// Add 表示增加一个新的地址
	Add Operation = iota
	// Delete 表示删除一个已经存在的地址
	Delete
)

// Update 定义了一个name解析的更新
// 注意: Addr 和 Metadata同时为空是非法的
type Update struct {
	// Op 表示这个Update的操作
	Op Operation
	// Addr 表示已经更新的地址,如果没有地址更新则为空
	Addr string
	// Metadata 表示已经更新的metadata, 如果没有metadata的更新则为空
	// 传统的naming实现是不需要metadata的
	Metadata interface{}
}

// Resolver 创建了一个到目标target的Watcher, 可以追踪它的解析更新
type Resolver interface {
	// Resolver 创建一个target的Watcher
	Resolve(target string) (Watcher, error)
}

// Watcher 观察指定target的变化
type Watcher interface {
	// Next 是一个阻塞行为,直到有一个update或者error发生
	// 它可能返回一个或多个updates
	// 第一次调用需要获取结果的完成集合
	// 只有当watcher发生错误不能恢复时才返回错误
	Next() ([]*Update, error)
	// Close 关闭watcher
	Close()
}
