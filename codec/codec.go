package codec

import "io"

//请求和响应中的参数和返回值抽象为 body

//剩余的信息放在 header 中
type Header struct {
	ServiceMethod string //service.method
	Seq uint64 //请求序号
	Error string //错误消息，client发起时为空
}
//抽象出对消息体进行编解码的接口 Codec
type CodeC interface {
	io.Closer
	ReadHeader (*Header) error
	ReadBody (interface{})error
	Write(*Header,interface{})error
}
//抽象出 Codec 的构造函数,通过传入Closer的实现
type NewCodeFunc func(io.ReadWriteCloser)CodeC

type Type string

const(
	GodType Type= "application/gob"
	JsonType Type = "application/json" // not implemented
)
//客户端和服务端可以通过 Codec 的 Type 得到构造函数
var NewCodeFuncMap map[Type]NewCodeFunc

func init()  {
	NewCodeFuncMap = make(map[Type]NewCodeFunc)
	NewCodeFuncMap[GodType] = NewGobCodec //god这个类型的codec的构造函数
}