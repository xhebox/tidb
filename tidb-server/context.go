package main

import (
	"context"
	"time"
)

type Context struct {
	context.Context
	params Parameters
	vals   map[string]interface{}
}

func Background() *Context {
	return &Context{
		Context: context.Background(),
	}
}

func WithCancel(parent *Context) (nctx *Context, cancel context.CancelFunc) {
	nctx = parent.Clone()
	nctx.Context, cancel = context.WithCancel(nctx.Context)
	return
}

func WithDeadline(parent *Context, d time.Time) (nctx *Context, cancel context.CancelFunc) {
	nctx = parent.Clone()
	nctx.Context, cancel = context.WithDeadline(nctx.Context, d)
	return
}

func WithTimeout(parent *Context, timeout time.Duration) (nctx *Context, cancel context.CancelFunc) {
	nctx = parent.Clone()
	nctx.Context, cancel = context.WithTimeout(nctx.Context, timeout)
	return
}

func (ctx *Context) Clone() *Context {
	nctx := *ctx
	newvals := make(map[string]interface{})
	for k, v := range nctx.vals {
		newvals[k] = v
	}
	nctx.vals = newvals
	return &nctx
}

func (ctx *Context) Set(k string, v interface{}) *Context {
	ctx.vals[k] = v
	return ctx
}

func (ctx *Context) Get(k string) (interface{}, bool) {
	v, ok := ctx.vals[k]
	return v, ok
}
