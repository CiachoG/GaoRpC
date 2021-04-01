package geerpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

const MagicNumber = 0x3bef5c

//协议协商
type Option struct {
	MagicNumber int  //标记是gee rpc的请求
	CodeType codec.Type //编解码类型
	ConnectTimeout time.Duration // 0 means no limit
	HandleTimeout  time.Duration
}
/***
一次连接中，报文的样式
|option（json）|Header1|Body1|Header2|Body2|……
 */
var DefaultOption = &Option{ //为了方便Option采用json编码
	MagicNumber:MagicNumber,
	CodeType: codec.GodType,//header和body采用的编解码方式
	ConnectTimeout: time.Second * 10,
}

func NewServer()*Server  {
	return &Server{}
}

var DefaultServer = NewServer()
func Accept(listener net.Listener)  {
	DefaultServer.Accept(listener)
}
func Register(rcvr interface{}) error { return DefaultServer.Register(rcvr) }




//RPC server
type Server struct {
	serviceMap sync.Map
}

func (server *Server) Register(rcvr interface{})error {
	s := newService(rcvr)
	if _,dup := server.serviceMap.LoadOrStore(s.name,s);dup {
		return errors.New("rpc: service already defined: " + s.name)
	}
	return nil
}

func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc server: service/method request ill-formed: " + serviceMethod)
		return
	}
	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server: can't find service " + serviceName)
		return
	}
	svc = svci.(*service)
	mtype = svc.method[methodName]
	if mtype == nil {
		err = errors.New("rpc server: can't find method " + methodName)
	}
	return
}

func (server *Server) Accept(listener net.Listener)  {
	for {
		conn,err := listener.Accept()
		if err != nil {
			log.Println("rpc server:accept error:",err)
			return
		}
		go server.ServeConn(conn)//开启子协程处理
	}
}
func (server *Server)ServeConn(conn io.ReadWriteCloser){
	var opt Option
	if err := json.NewDecoder(conn).Decode(&opt);err != nil  {
		log.Println("rpc server:options error:",err.Error())
		return
	}
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server:invalid magic number %x",opt.MagicNumber)
		return
	}
	f := codec.NewCodeFuncMap[opt.CodeType]
	if f == nil {
		log.Printf("rpc server:invalid codec type %s",opt.CodeType)
		return
	}
	server.serveCodec(f(conn),&opt) //传入构造函数
}
var invalidRequest = struct {}{}

func (server *Server)serveCodec(cc codec.CodeC, opt *Option)  {
	sending := new(sync.Mutex)
	wg := new(sync.WaitGroup)
	for {
		req,err := server.readRequest(cc) //读取请求
		if err != nil {
			if req == nil {
				break
			}
			req.h.Error = err.Error() //把error信息写入header中
			server.sendResponse(cc,req.h,invalidRequest,sending) //发生错误时返回
			continue
		}
		wg.Add(1)
		go server.handleRequest(cc,req,sending,wg,opt.HandleTimeout)
	}
	wg.Wait()
	_ = cc.Close()
}
//服务端读取请求
type request struct {
	h * codec.Header
	argv,replyv reflect.Value //未知参数和返回的类型
	mType *methodType
	svc          *service
}

func (server *Server)readRequestHeader(cc codec.CodeC)(*codec.Header,error)  {
	var h codec.Header
	if err := cc.ReadHeader(&h);err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server:read header error:",err)
		}
		return nil, err
	}
	return &h,nil
}
func (server *Server)readRequest(cc codec.CodeC)(*request,error)  {
	h,err := server.readRequestHeader(cc)
	if err != nil {
		return nil,err
	}
	req := &request{h: h}
	req.svc,req.mType,err = server.findService(h.ServiceMethod)
	if err != nil {
		return nil, err
	}
	req.argv = req.mType.newArgv()
	req.replyv = req.mType.newReplyV()
	//确保argvi是一个指针
	argvi := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Ptr {
		argvi = req.argv.Addr().Interface()
	}
	if err = cc.ReadBody(argvi);err != nil {
		log.Println("rpc server:read argv err:",err)
		return req, err
	}
	return req, err
}
//异步调用call
func (server *Server)handleRequest(cc codec.CodeC,req *request,sending *sync.Mutex,wg *sync.WaitGroup,timeout time.Duration)  {
	defer wg.Done()
	called := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		err := req.svc.call(req.mType,req.argv,req.replyv)
		called <- struct{}{}
		if err != nil {
			req.h.Error = err.Error()
			server.sendResponse(cc,req.h,invalidRequest,sending)
			sent <- struct{}{}
			return
		}
		server.sendResponse(cc,req.h,req.replyv.Interface(),sending)
		sent <- struct{}{}
	}()
	if timeout ==0 { //不限时
		<-called
		<- sent
		return
	}
	select {
	case <- time.After(timeout):
		req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
		server.sendResponse(cc, req.h, invalidRequest, sending)
	case <-called:
		<-sent
	}

}
func (server *Server)sendResponse(cc codec.CodeC,h *codec.Header,body interface{},sending *sync.Mutex)  {
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(h,body);err != nil {
		log.Println("rpc server:write response error:",err)
	}
}
// --------http req ->> rpc req --------
const (
	connected        = "200 Connected to Gee RPC"
	defaultRPCPath   = "/_geeprc_"
	defaultDebugPath = "/debug/geerpc"
)
// ServeHTTP implements an http.Handler that answers RPC requests.
func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405 must CONNECT\n")
		return
	}
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking ", req.RemoteAddr, ": ", err.Error())
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
	server.ServeConn(conn)
}
func (server *Server) HandleHTTP() {
	http.Handle(defaultRPCPath, server) //注册默认路径
	http.Handle(defaultDebugPath,debugHTTP{server})
	log.Println("rpc server debug path:", defaultDebugPath)
}
func HandleHTTP() {
	DefaultServer.HandleHTTP()
}


