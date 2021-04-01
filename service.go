package geerpc

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

type methodType struct {
	method reflect.Method //方法本身
	ArgType reflect.Type //第一个参数的类型
	ReplyType reflect.Type //第二个参数的类型
	numCalls uint64 //用于统计调用次数
}
func (m *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&m.numCalls)
}
func (m *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	// arg may be a pointer type, or a value type
	if m.ArgType.Kind() == reflect.Ptr {
		argv = reflect.New(m.ArgType.Elem()) //创建指针类型
	} else {
		argv = reflect.New(m.ArgType).Elem() //创建值类型
	}
	return argv
}
func (m *methodType)newReplyV()reflect.Value  {
	replyV := reflect.New(m.ReplyType.Elem())
	switch m.ReplyType.Elem().Kind() {
	case reflect.Map:
		replyV.Elem().Set(reflect.MakeMap(m.ReplyType.Elem()))
	case reflect.Slice:
		replyV.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(),0,0))
	}
	return replyV
}

type service struct {
	name string  //映射的结构体的名称
	typ reflect.Type //结构体的类型
	rcvr reflect.Value //结构体的实例本身
	method map[string]*methodType //存储结构体所有符合条件的方法
}

func newService(rcvr interface{})*service  {
	s := new(service)
	s.rcvr = reflect.ValueOf(rcvr)
	s.name = reflect.Indirect(s.rcvr).Type().Name() //返回值
	s.typ = reflect.TypeOf(rcvr)
	if !ast.IsExported(s.name) { //不是导出的
		log.Fatalf("rpc server: %s is not a valid service name",s.name)
	}
	s.registerMethods()
	return s
}
func (s *service)registerMethods()  {
	s.method = make(map[string]*methodType)
	for i := 0;i<s.typ.NumMethod();i++{
		method := s.typ.Method(i)
		mType := method.Type
		if mType.NumIn() != 3 || mType.NumOut() != 1 { //检查rpc方法的格式,in有3个，第一个是自身，类似于py的self 和 java的this
			continue
		}
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() { //Elem()返回元素的类型
			continue
		}
		argType,replyType := mType.In(1),mType.In(2)
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {//两个入参，均为导出或内置类型。
			continue
		}
		s.method[method.Name] = &methodType{
			method: method,
			ArgType: argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server:register %s.%s\n",s.name,method.Name)

	}
}
func isExportedOrBuiltinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}
func (s *service)call(m *methodType,argv,replyV reflect.Value)error  {
	atomic.AddUint64(&m.numCalls,1)
	f := m.method.Func
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyV})
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}