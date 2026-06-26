package dsprobe

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// probeMongoDB 用 mongo-driver Connect + Ping。
//
// 关键陷阱:mongo-driver 的 ApplyURI() 内部会对 user/password 做 URL 解码,
// 用户密码若含 `< ] ^ @` 等特殊字符且未在 URI 里编码,会被截断 / 破坏 → 服务端报
// "Authentication failed" 但密码其实是对的。
//
// 解法:**手工拆 URI** 把 host/db/authSource 喂给 ApplyURI(不带凭证),
// 用户名密码用 SetAuth(Credential{...}) 单独传 —— SDK 不再对密码做编码 / 解码,
// 原文 SCRAM。这样无论密码多怪都能通。
func probeMongoDB(f map[string]string) (bool, string, error) {
	opts, err := mongoClientOptionsFromFields(f)
	if err != nil {
		return false, "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cli, err := mongo.Connect(ctx, opts)
	if err != nil {
		return false, "", mongoErrorMsg(err)
	}
	defer func() { _ = cli.Disconnect(context.Background()) }()
	if err := cli.Ping(ctx, readpref.Primary()); err != nil {
		return false, "", mongoErrorMsg(err)
	}
	var doc map[string]any
	_ = cli.Database("admin").RunCommand(ctx, map[string]any{"buildInfo": 1}).Decode(&doc)
	ver := "MongoDB"
	if v, ok := doc["version"].(string); ok {
		ver = "MongoDB " + v
	}
	return true, "登录 OK · " + ver, nil
}

func mongoClientOptionsFromFields(f map[string]string) (*options.ClientOptions, error) {
	uri := strings.TrimSpace(f["uri"])
	if uri == "" {
		uri = strings.TrimSpace(f["url"])
	}

	var rawUser, rawPass, hostPart, dbPart, queryPart, authSource string

	if uri != "" {
		// 手工拆,不走 url.Parse(它对密码也会做解码)
		s := uri
		if i := strings.Index(s, "://"); i >= 0 {
			s = s[i+3:]
		}
		// userinfo 与 host 用最后一个 @ 分隔(host 部分不会含 @)
		hostAndPath := s
		if at := strings.LastIndex(s, "@"); at >= 0 {
			ui := s[:at]
			hostAndPath = s[at+1:]
			// user:pass —— 第一个 : 之前是 user
			if c := strings.Index(ui, ":"); c >= 0 {
				rawUser = ui[:c]
				rawPass = ui[c+1:]
			} else {
				rawUser = ui
			}
		}
		// host[:port] / db ? query
		if slash := strings.Index(hostAndPath, "/"); slash >= 0 {
			hostPart = hostAndPath[:slash]
			rest := hostAndPath[slash+1:]
			if q := strings.Index(rest, "?"); q >= 0 {
				dbPart = rest[:q]
				queryPart = rest[q+1:]
				for _, kv := range strings.Split(queryPart, "&") {
					if strings.HasPrefix(kv, "authSource=") {
						authSource = strings.TrimPrefix(kv, "authSource=")
					}
				}
			} else {
				dbPart = rest
			}
		} else {
			hostPart = hostAndPath
		}
	} else {
		// 用 host/port/user/password/database 字段拼
		host := strings.TrimSpace(f["host"])
		if host == "" {
			return nil, errors.New("缺 uri 或 host")
		}
		port := strings.TrimSpace(f["port"])
		if port == "" {
			port = "27017"
		}
		hostPart = host + ":" + port
		rawUser = strings.TrimSpace(f["user"])
		if rawUser == "" {
			rawUser = strings.TrimSpace(f["username"])
		}
		rawPass = f["password"]
		dbPart = strings.TrimSpace(f["database"])
	}

	if hostPart == "" {
		return nil, errors.New("无法从 uri 解析出 host")
	}
	// 拼"安全 URI"(不含凭证),凭证走 SetAuth 不经过 URL 解析。
	// query 需要原样保留,否则 directConnection=true / replicaSet / tls 等连接行为会丢。
	safeURI := "mongodb://" + hostPart
	if dbPart != "" {
		safeURI += "/" + dbPart
	}
	if queryPart != "" {
		safeURI += "?" + queryPart
	}

	opts := options.Client().ApplyURI(safeURI).
		SetConnectTimeout(probeTimeout).
		SetServerSelectionTimeout(probeTimeout)
	if rawUser != "" {
		cred := options.Credential{Username: rawUser, Password: rawPass}
		// authSource 优先用 URI 里 query;没指定时 mongo-driver 默认走 connect db
		// 这里不强加 admin —— 若用户的 root 真建在 connect db 里,加 admin 反而会错
		if authSource != "" {
			cred.AuthSource = authSource
		}
		opts.SetAuth(cred)
	}
	return opts, nil
}

func mongoErrorMsg(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Authentication failed"):
		return fmt.Errorf("账号密码错: %s", msg)
	case strings.Contains(msg, "Unauthorized"):
		return fmt.Errorf("权限不足: %s", msg)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("连接被拒: %s", msg)
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "context deadline"):
		return fmt.Errorf("连接 / 选举超时(地址错 / VPC 不通?): %s", msg)
	}
	return err
}
