package main

import (
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/xiaolong/troubleshooter-studio/api"
	"github.com/xiaolong/troubleshooter-studio/internal/webui"
)

type serveOptions struct {
	addr              string
	readHeaderTimeout time.Duration
}

func parseServeFlags(args []string) (serveOptions, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	opts := serveOptions{readHeaderTimeout: 5 * time.Second}
	fs.StringVar(&opts.addr, "addr", "127.0.0.1:8080", "listen address")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	return opts, nil
}

func runServe(args []string) error {
	opts, err := parseServeFlags(args)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr:              opts.addr,
		Handler:           newServeHandler(resolveTemplateDir()),
		ReadHeaderTimeout: opts.readHeaderTimeout,
	}
	fmt.Printf("tshoot serve listening on http://%s\n", opts.addr)
	return httpSrv.ListenAndServe()
}

func newServeHandler(templateRoot string) http.Handler {
	srv := &api.Server{TemplateRoot: templateRoot}
	return api.NewRouter(srv, webui.Distribution())
}
