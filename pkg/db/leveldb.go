package db

import (
	"encoding/json"
	"flag"
	"github.com/sirupsen/logrus"
	"github.com/syndtr/goleveldb/leveldb"
)

const (
	defaultDBPath = "/var/lib/hostnic"
)

var LevelDB *leveldb.DB

type LevelDBOptions struct {
	DBPath string
}

func NewLevelDBOptions() *LevelDBOptions {
	return &LevelDBOptions{
		DBPath: defaultDBPath,
	}
}

func (opt *LevelDBOptions) AddFlags() {
	flag.StringVar(&opt.DBPath, "dbpath", defaultDBPath, "set leveldb path")
}

func InitLevelDB(opt *LevelDBOptions) {
	db, err := leveldb.OpenFile(opt.DBPath, nil)
	if err != nil {
		logrus.Fatalf("cannot open file %s, err=%s", opt.DBPath, err)
	}

	LevelDB = db
}

func CloseDB() {
	LevelDB.Close()
}

type NetworkInfo struct {
	K8S_POD_NAME      string `json:"K8S_POD_NAME,omitempty"`
	K8S_POD_NAMESPACE string `json:"K8S_POD_NAMESPACE,omitempty"`
	Netns             string `json:"Netns,omitempty"`
	IfName            string `json:"IfName,omitempty"`
	IPv4Addr          string `json:"IPv4Addr,omitempty"`
	NicID             string `json:"NicID,omitempty"`
}

func SetNetworkInfo(key string, info *NetworkInfo) error {
	value, _ := json.Marshal(info)
	return LevelDB.Put([]byte(key), value, nil)
}

func DeleteNetworkInfo(key string) error {
	return LevelDB.Delete([]byte(key), nil)
}

func FindPodByNic(mac string) (*NetworkInfo, error) {
	var result *NetworkInfo

	iter := LevelDB.NewIterator(nil, nil)
	for iter.Next() {
		var (
			value NetworkInfo
		)
		// Remember that the contents of the returned slice should not be modified, and
		// only valid until the next call to Next.
		json.Unmarshal(iter.Value(), &value)

		if value.NicID == mac {
			result = &value
			break
		}
	}
	iter.Release()
	err := iter.Error()
	if err != nil {
		return nil, err
	}

	return result, nil
}
