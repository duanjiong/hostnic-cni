package ipam

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"github.com/yunify/hostnic-cni/pkg/types"
)

type IpamConf struct {
	Pool   PoolConf   `json:"pool,omitempty" yaml:"pool,omitempty"`
	Server ServerConf `json:"server,omitempty" yaml:"server,omitempty"`
}

type PoolConf struct {
	PoolHigh       int         `json:"poolhigh,omitempty" yaml:"poolhigh,omitempty"`
	PoolLow        int         `json:"poollow,omitempty" yaml:"poollow,omitempty"`
	MaxNic         int         `json:"maxnic,omitempty" yaml:"maxnic,omitempty"`
	Delay          int         `json:"delay,omitempty" yaml:"delay,omitempty"`
	RouteTableBase int         `json:"routeTableBase,omitempty" yaml:"routeTableBase,omitempty"`
	Vxnet          []VxnetConf `json:"vxnets,omitempty" yaml:"vxnets,omitempty"`
	Cool           int
}

type ServerConf struct {
	ServerPath string `json:"serverpath,omitempty" yaml:"serverpath,omitempty"`
}

type VxnetConf struct {
	Vxnet    string `json:"vxnet,omitempty" yaml:"vxnet,omitempty"`
	PoolHigh int    `json:"poolhigh,omitempty" yaml:"poolhigh,omitempty"`
	PoolLow  int    `json:"poollow,omitempty" yaml:"poollow,omitempty"`
}

// TryLoadFromDisk loads configuration from default location after server startup
// return nil error if configuration file not exists
func TryLoadFromDisk(name, path string) *IpamConf {
	viper.SetConfigName(name)
	viper.AddConfigPath(".")
	viper.AddConfigPath(path)

	conf := &IpamConf{
		Pool: PoolConf{
			PoolHigh:       types.DefaultMaxPoolSize,
			PoolLow:        types.DefaultPoolSize,
			MaxNic:         types.NicNumLimit,
			Delay:          types.DefaultPoolSyn,
			RouteTableBase: types.DefaultRouteTableBase,
			Cool:           types.DefaultNicCool,
		},
		Server: ServerConf{
			ServerPath: types.DefaultSocketPath,
		},
	}

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			log.WithError(err).Infof("use defualt value %+v", *conf)
			return conf
		} else {
			panic(fmt.Errorf("error parsing configuration file %s", err))
		}
	}

	if err := viper.Unmarshal(conf); err != nil {
		panic(err)
	}

	if err := validateConf(conf); err != nil {
		panic(err)
	}

	log.Infof("use defualt value %+v", *conf)
	return conf
}


func validateConf(conf *IpamConf) error {
	if conf.Pool.PoolLow > conf.Pool.PoolHigh {
		return fmt.Errorf("PoolLow should less than PoolHigh")
	}
	return nil
}