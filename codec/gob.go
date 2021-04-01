package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

//gob是go语言内置的编解码包，可以支持变长类型的编解码（意味着通用）
type GobCodec struct {
	coon io.ReadWriteCloser //tcp或者unix建立socket时得到的连接实例
	buf *bufio.Writer//带缓冲的writer，防止阻塞
	dec *gob.Decoder //gob解码器
	enc *gob.Encoder //gob编码器
}
var _Codec = (*GobCodec)(nil)

func NewGobCodec(conn io.ReadWriteCloser)CodeC  {
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		coon: conn,
		buf: buf,
		dec: gob.NewDecoder(conn),
		enc: gob.NewEncoder(buf),
	}
}

func (c *GobCodec)ReadHeader(h *Header)error  {
	return c.dec.Decode(h) //解码header
}
func (c *GobCodec ) ReadBody(body interface{}) error {
	return c.dec.Decode(body) //解码body
}
func (c *GobCodec)Write(h *Header,body interface{})(err error)  {
	defer func() {
		_ = c.buf.Flush() //flush
		if err != nil {
			_ = c.Close()
		}
	}()
	if err := c.enc.Encode(h);err != nil{ //编码header
		log.Println("rpc codec:gob error encoding header:",err)
		return err
	}
	if err := c.enc.Encode(body);err != nil {//编码body
		log.Println("rpc codec:gob error encoding body:",err)
		return err
	}
	return nil
}
func (c *GobCodec)Close() error {
	return c.coon.Close()
}