package http

func register(r Router) { r.POST("/internal/orders", createOrder) }
