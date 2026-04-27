// connpool.go —— nacos client 进程内缓存。
//
// Studio 里每个用户操作(Step 5 预加载两阶段、Step 7 批量 fetch)都可能多次调到
// PreloadNacos / FetchContent;每次都 probe 4 路径 + login 一次 = 5 次 HTTP 往返,
// 对反代层压力大、也慢。实际上凭证没变,可以一次 probe+login,后续复用同一个 client。
//
// 策略:
//  - 按 (type, addr, username, password) 做 cache key(token 模式的 apollo/consul 也能放)
//  - 命中 → 直接复用;未命中 → connect 一次 → 缓存
//  - 缓存有效期:Nacos 默认 tokenTtl=18000s(5h),我们保守用 30 分钟就过期重建
//  - 凭证改动 → cache key 变 → 自然不命中 → 重建。无需手动 invalidate
//  - 进程级,不跨进程共享;Studio 自身是桌面单机应用,不需要分布式
//
// 线程安全:sync.Map 读写(多窗口同时点可能并发)+ 建立连接用 sync.Mutex 避免同 key 同时
// connect 两次(第二次进等待,第一次完成后直接复用)。
package cchub

import (
	"sync"
	"time"
)

type cachedClient struct {
	cli      *nacosClient
	expireAt time.Time
}

var (
	nacosConnCache sync.Map   // cacheKey → *cachedClient
	nacosConnMu    sync.Mutex // 单 key 并发 connect 的串行锁(粗粒度 OK,建连本身不频繁)
)

const nacosConnTTL = 30 * time.Minute

func nacosCacheKey(addr, user, pass string) string {
	// 用 \x1f(ASCII Unit Separator)分隔,防止字段里带 | 等字符误合并
	return addr + "\x1f" + user + "\x1f" + pass
}

// getOrConnectNacos 返回一个已 probe+login 的 nacosClient。命中 cache 直接返;未命中 connect 一次。
// expired 或失败时会重建。调用方不要修改 client.flavor / token 字段,不然会影响其他并发调用。
func getOrConnectNacos(addr, user, pass string) (*nacosClient, error) {
	key := nacosCacheKey(addr, user, pass)
	if v, ok := nacosConnCache.Load(key); ok {
		c := v.(*cachedClient)
		if time.Now().Before(c.expireAt) {
			return c.cli, nil
		}
	}
	nacosConnMu.Lock()
	defer nacosConnMu.Unlock()
	// double-check:可能另一个 goroutine 已经 connect 过
	if v, ok := nacosConnCache.Load(key); ok {
		c := v.(*cachedClient)
		if time.Now().Before(c.expireAt) {
			return c.cli, nil
		}
	}
	cli, err := connectNacos(addr, user, pass)
	if err != nil {
		return nil, err
	}
	nacosConnCache.Store(key, &cachedClient{
		cli:      cli,
		expireAt: time.Now().Add(nacosConnTTL),
	})
	return cli, nil
}

// invalidateNacosConn 手动清某组凭证的缓存。目前没有直接入口(token 过期靠 TTL 自动重建),
// 保留这个以备将来加 "用户改密码 / 强制重连" 的 UI。
func invalidateNacosConn(addr, user, pass string) {
	nacosConnCache.Delete(nacosCacheKey(addr, user, pass))
}
