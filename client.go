package geerpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Call struct {
	Seq uint64 //序号
	ServiceMethod string //format 服务.方法
	Args interface{} // 参数
	Reply interface{} //返回
	Error error  //错误
	Done  chan *Call  //支持异步调用，当调用结束时，会调用call.done通知对方
}

func (call *Call) done()  {
	call.Done <-call
}

//client 客户端核心结构，可能被多个go routine同时调用
type Client struct {
	cc codec.CodeC //编解码器
	opt *Option //协议协商
	header codec.Header //每个请求的消息头，只在请求发送时才需要，请求是互斥的，因此每个客户端仅需要一个
	sending sync.Mutex //和server类似，用来保证请求的有序发送，防止多个报文混淆
	mu sync.Mutex
	seq uint64 //发送的编号，具有唯一性
	pending map[uint64]*Call//存储未处理完的请求，键值是编号
	closing bool //用户主动关闭
	shutdown bool //发生错误时关闭
}
var _ io.Closer = (*Client)(nil)

var ErrShutdown = errors.New("connection is shutdown")

type clientResult struct {
	client *Client
	err error
}
type newClientFunc func(conn net.Conn,opt *Option)(client *Client,err error)


func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing { //防止重复关闭
		return ErrShutdown
	}
	client.closing = true
	return  client.cc.Close() //关闭连接
}
func (client *Client) IsAvailable()bool{
	client.mu.Lock()
	defer client.mu.Unlock()
	return !(client.shutdown || client.closing)
}

//与Call相关的三个方法
func (client *Client)registerCall(call *Call)(uint64,error)  {
	client.mu.Lock()
	defer client.mu.Unlock()
	//if !client.IsAvailable(){ //不可用状态，锁重入，造成死锁
	//	return 0,ErrShutdown
	//}
	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq ++
	return call.Seq,nil
}
//根据seq从 client.pending 中移除对应的 call，并返回
func (client *Client)removeCall(seq uint64)*Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending,seq)
	return call
}
//terminateCalls服务端或客户端发生错误时调用，将 shutdown 设置为 true，且将错误信息通知所有 pending 状态的 call。
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

//接受
func (client *Client)receive() {
	var err error
	for err == nil{
		var h codec.Header
		if err = client.cc.ReadHeader(&h);err != nil {
			break
		}
		call := client.removeCall(h.Seq)
		switch  {
		case call==nil: //可能是请求未发完整或者其他原因取消，seq有误
			err = client.cc.ReadBody(nil)
		case h.Error!= ""://服务端出错
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			err = client.cc.ReadBody(call.Reply)
			if err != nil{
				call.Error = errors.New("reading body"+err.Error())
			}
			call.done()
		}

	}
	client.terminateCalls(err)
}

//发送
func (client *Client)send(call *Call)  {
	client.sending.Lock()
	defer client.sending.Unlock()

	seq,err := client.registerCall(call) //注册一个call
	if err != nil {
		call.Error = err
		call.done()
		return
	}
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""
	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}
//异步接口
func (client *Client)Go(serviceMethod string,args,reply interface{},done chan *Call)*Call {
	if done == nil {
		done = make(chan *Call,10)
	}else if cap(done) == 0{
		log.Panic("rpc client:done channel is unbuffered")
	}
	call := &Call{
		ServiceMethod: serviceMethod,
		Args: args,
		Reply: reply,
		Done: done,
	}
	client.send(call)
	return call
}
//同步接口,新增超时控制
//context.WithTimeout 创建具备超时检测能力的 context 对象来控制
func (client *Client)Call(ctx context.Context,serviceMethod string,args,reply interface{})error  {
	call := client.Go(serviceMethod,args,reply,make(chan *Call,1))
	select {
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client:call failed:"+ctx.Err().Error())
	case call := <- call.Done:
		return call.Error
		
	}

	return call.Error
}



//协议交换，即发送Option信息给服务端
func NewClient(conn net.Conn,opt *Option)(*Client,error)  {
	//获取编解码器构造方法
	f := codec.NewCodeFuncMap[opt.CodeType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s",opt.CodeType)
		log.Println("rpc client:codec error:",err)
		return nil,err
	}
	if err := json.NewEncoder(conn).Encode(opt);err != nil {
		log.Println("rpc client:options error:",err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn),opt),nil
}
func newClientCodec(cc codec.CodeC,opt *Option)*Client  {
	client := &Client{
		seq: 1,
		cc: cc,
		opt: opt,
		pending: make(map[uint64]*Call),
	}
	go client.receive() //开启子协程接受响应
	return client
}
func parseOptions(opts ...*Option)(*Option,error)  {
	if len(opts) == 0 || opts[0]== nil {
		return DefaultOption,nil
	}
	if len(opts) != 1 {
		return nil,errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodeType == "" {
		opt.CodeType = DefaultOption.CodeType
	}
	return opt,nil
}

func dialTimeout(f newClientFunc,network,address string,opts ...*Option)(client *Client,err error)  {
	opt,err := parseOptions(opts...)
	if err != nil {
		return nil,err
	}
	conn,err := net.DialTimeout(network,address,opt.ConnectTimeout)
	if err != nil {
		return nil,err
	}
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	ch := make(chan clientResult)
	go func() { // 创建子协程完成连接
		client,err := f(conn,opt)
		ch <- clientResult{client: client,err: err}
	}()

	if opt.ConnectTimeout == 0{ //没有设置连接超时
		result := <-ch
		return result.client,result.err
	}
	select {
	case <-time.After(opt.ConnectTimeout):
		return nil,fmt.Errorf("rpc client:connect timeout:expect within ")
	case result := <- ch:
		return result.client,result.err
	}

}
func Dial(network,address string,opts ...*Option)(client *Client,err error)  {
	return dialTimeout(NewClient,network,address,opts...)
}


// NewHTTPClient new a Client instance via HTTP as transport protocol
func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
	_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))

	// Require successful HTTP response
	// before switching to RPC protocol.
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err == nil && resp.Status == connected {
		return NewClient(conn, opt)
	}
	if err == nil {
		err = errors.New("unexpected HTTP response: " + resp.Status)
	}
	return nil, err
}

// DialHTTP connects to an HTTP RPC server at the specified network address
// listening on the default HTTP RPC path.
func DialHTTP(network, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewHTTPClient, network, address, opts...)
}

// XDial calls different functions to connect to a RPC server
// according the first parameter rpcAddr.
// rpcAddr is a general format (protocol@addr) to represent a rpc server
// eg, http@10.0.0.1:7001, tcp@10.0.0.1:9999, unix@/tmp/geerpc.sock
func XDial(rpcAddr string, opts ...*Option) (*Client, error) {
	parts := strings.Split(rpcAddr, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client err: wrong format '%s', expect protocol@addr", rpcAddr)
	}
	protocol, addr := parts[0], parts[1]
	switch protocol {
	case "http":
		return DialHTTP("tcp", addr, opts...)
	default:
		// tcp, unix or other transport protocol
		return Dial(protocol, addr, opts...)
	}
}

