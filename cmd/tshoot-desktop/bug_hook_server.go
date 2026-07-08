package main

import (
	"fmt"
	"net"
	"net/http"

	"github.com/xiaolong/troubleshooter-studio/api"
)

func startBugHookReceiver(templateRoot string) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	srv := &http.Server{
		Handler: api.NewRouter(&api.Server{TemplateRoot: templateRoot}, nil),
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[warn] bug hook receiver stopped: %v\n", err)
		}
	}()
	return "http://" + ln.Addr().String(), nil
}
