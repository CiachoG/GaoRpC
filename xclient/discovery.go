package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

type SelectMode int //负载均衡策略

const (
	RandomSelect  SelectMode  =iota //随机
	RoundRobinSelect //轮询
)

//服务发现接口

type Discovery interface {
	Refresh() error //从注册中心更新服务列表
	Update(servers []string) error //手动更新服务列表
	Get(mode SelectMode)(string,error) //根据负载均衡策略选择一个服务实例
	GetAll()([]string,error) //获得所有服务实例
}


// MultiServersDiscovery is a discovery for multi servers without a registry center
// user provides the server addresses explicitly instead
type MultiServersDiscovery struct {
	r       *rand.Rand   // generate random number
	mu      sync.RWMutex // protect following
	servers []string
	index   int // record the selected position for robin algorithm
}
func NewMultiServerDiscovery(servers []string) *MultiServersDiscovery {
	d := &MultiServersDiscovery{
		servers: servers,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	d.index = d.r.Intn(math.MaxInt32 - 1)
	return d
}

var _Discovery = (*MultiServersDiscovery)(nil)


//MultiServersDiscovery 实现 Discovery接口

func (d *MultiServersDiscovery)Refresh()error  {
	return nil
}
func (d *MultiServersDiscovery)Update(servers []string)error  {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	return nil
}
func (d *MultiServersDiscovery) Get(mode SelectMode)(string,error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(d.servers)
	if n == 0  {
		return "",errors.New("rpc discovery: no available servers")
	}
	switch mode {
	case RandomSelect:
		return d.servers[d.r.Intn(n)],nil
	case RoundRobinSelect:
		s := d.servers[d.index % n]
		d.index = (d.index + 1)%n
		return s,nil
	default:
		return "", errors.New("rpc discovery: not supported select mode")
	}
}
func (d *MultiServersDiscovery) GetAll() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	// return a copy of d.servers
	servers := make([]string, len(d.servers), len(d.servers))
	copy(servers, d.servers)
	return servers, nil
}