package grpc

// ServiceConfig 由service的提供者提供,包含了一些决定了client连接到service的行为的参数
// 实验性接口, 未来可能会变
type ServiceConfig struct {
	// LB是service提供方推荐的load blancer
	// 使用grpc.WithBalancer指定的balancer会覆盖这个
	LB Balancer

	//Methods 包含了service中方法的map
	//如果在map中存在一个和method精确的匹配项(如 /service/method), 则使用对应的serviceConfig
	//如果没有精确匹配的,则使用默认的method的config(/service/)
	//否则就没有对应的serverConfig使用
	Methods map[string]MethodConfig
}
